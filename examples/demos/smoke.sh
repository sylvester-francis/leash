#!/usr/bin/env bash
# End-to-end smoke test (used by CI, not a showcase demo). Brings up the fake
# upstream and a governed gateway, then asserts the whole path works: a call is
# metered, the cost budget trips with the right 429 body, an unpriceable
# server-side tool request fails closed (server_tool_unpriced), and the admin
# surface (health, readiness, metrics) responds. Exits non-zero on any mismatch.
source "$(dirname "$0")/_lib.sh"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$GATE"; demo_require_free "$ADMIN"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 4000 --completion-tokens 2000
demo_wait "$UPSTREAM"

export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"
demo_bg "$LEASH" serve --listen "$GATE" --admin "$ADMIN" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 0.25
demo_wait "$GATE"; demo_wait "$ADMIN"

post_code() {
  curl -s -o "$DEMO_TMP/r" -w '%{http_code}' -X POST "http://$GATE/v1/chat/completions" \
    -H "X-Leash-Token: $LEASH_AUTH_TOKEN" -H "X-Loop-Id: smoke" -d '{"model":"demo-model"}'
}

# The first call is metered and allowed.
[ "$(post_code)" = "200" ] || fail "first call was not 200 (got $(cat "$DEMO_TMP/r"))"

# At $0.10/call under a $0.25 budget, a later call must be refused.
code=""
for _ in 1 2 3 4 5 6; do code="$(post_code)"; [ "$code" = "429" ] && break; done
[ "$code" = "429" ] || fail "cost budget never tripped (last status $code)"
grep -q '"reason":"cost_budget"' "$DEMO_TMP/r" || fail "429 body missing cost_budget reason: $(cat "$DEMO_TMP/r")"

# Admin surface.
[ "$(curl -s "http://$ADMIN/healthz")" = "ok" ] || fail "/healthz did not return ok"
curl -s "http://$ADMIN/readyz" | grep -q ready || fail "/readyz did not return ready"
curl -s -H "X-Leash-Token: $LEASH_AUTH_TOKEN" "http://$ADMIN/metrics" \
  | grep -q '^leash_calls_total' || fail "/metrics missing leash_calls_total"

# Fail closed on unpriceable server-side tool spend: an Anthropic call that also
# billed a web search leash cannot price must stop the run (server_tool_unpriced).
GATE2=127.0.0.1:18090
UPSTREAM2=127.0.0.1:19097
demo_require_free "$GATE2"
demo_require_free "$UPSTREAM2"
demo_bg "$FAKEUP" --listen "$UPSTREAM2" --model demo-model \
  --prompt-tokens 100 --completion-tokens 50 --server-tool-requests 1
demo_wait "$UPSTREAM2"
demo_bg "$LEASH" serve --listen "$GATE2" --upstream "http://$UPSTREAM2" \
  --db "$DEMO_TMP/tools.db" --prices "$PRICES" --max-cost 5
demo_wait "$GATE2"

tool_post() {
  curl -s -o "$DEMO_TMP/tr" -w '%{http_code}' -X POST "http://$GATE2/v1/messages" \
    -H "X-Leash-Token: $LEASH_AUTH_TOKEN" -H "X-Loop-Id: tools" \
    -H "Anthropic-Version: 2023-06-01" -d '{"model":"demo-model"}'
}
[ "$(tool_post)" = "200" ] || fail "first tool call was not 200 (got $(cat "$DEMO_TMP/tr"))"
tcode=""
for _ in 1 2 3; do
  tcode="$(tool_post)"
  [ "$tcode" = "429" ] && break
done
[ "$tcode" = "429" ] || fail "unpriced server-tool spend did not stop the run (last status $tcode)"
grep -q '"reason":"server_tool_unpriced"' "$DEMO_TMP/tr" ||
  fail "429 body missing server_tool_unpriced: $(cat "$DEMO_TMP/tr")"

echo "SMOKE OK: metered a call, cost_budget tripped, server_tool_unpriced fail-closed, admin all responded"
