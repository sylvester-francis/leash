#!/usr/bin/env bash
# End-to-end smoke test (used by CI, not a showcase demo). Brings up the fake
# upstream and a governed gateway, then asserts the whole path works: a call is
# metered, the cost budget trips with the right 429 body, and the admin surface
# (health, readiness, metrics) responds. Exits non-zero on any mismatch.
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

echo "SMOKE OK: metered a call, cost_budget tripped, admin health/readiness/metrics all responded"
