#!/usr/bin/env bash
# Fail closed on unpriceable tool spend. An Anthropic call that also billed a
# server-side web search reports a per-request charge (usage.server_tool_use) that
# leash cannot price from the token table. Under a cost budget with the default
# --on-blind=refuse, that call is delivered once and then the run stops
# (server_tool_unpriced), so the uncounted spend cannot continue. Pricing the tool
# turns it into a metered charge instead.
source "$(dirname "$0")/_lib.sh"

GATE2=127.0.0.1:18090

demo_build
demo_require_free "$UPSTREAM"
demo_require_free "$GATE"
demo_require_free "$GATE2"
# Each call reports one web-search request leash cannot price.
demo_bg "$FAKEUP" --listen "$UPSTREAM" --model demo-model \
  --prompt-tokens 1000 --completion-tokens 500 --server-tool-requests 1
demo_wait "$UPSTREAM"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"

# Send Anthropic-shaped traffic so leash meters usage.server_tool_use.
anthropic_call() {
  local gate="$1" loop="$2"
  curl -s -o "$DEMO_TMP/resp" -w 'HTTP %{http_code}  ' -X POST "http://$gate/v1/messages" \
    -H "X-Leash-Token: $LEASH_AUTH_TOKEN" -H "X-Loop-Id: $loop" \
    -H "Anthropic-Version: 2023-06-01" -d '{"model":"demo-model"}'
  head -c 130 "$DEMO_TMP/resp"
  echo
}

demo_banner "Default (--on-blind=refuse): an unpriced web search stops the run"
demo_run "leash serve --max-cost 5 --prices prices.json   # demo-model has no web_search_per_request"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 5
demo_wait "$GATE"
for i in 1 2; do
  printf 'call %d  ' "$i"
  anthropic_call "$GATE" tools
done
echo "The first call is delivered (already billed), then the run stops: server_tool_unpriced."

demo_banner "Price the tool: now the web search is metered, and the run continues"
echo '{"demo-model": {"input": 10, "output": 30, "web_search_per_request": 0.01}}' >"$DEMO_TMP/tool-prices.json"
demo_run "leash serve --max-cost 5 --prices tool-prices.json   # web_search_per_request: 0.01"
demo_bg "$LEASH" serve --listen "$GATE2" --upstream "http://$UPSTREAM" \
  --db "$DEMO_TMP/priced.db" --prices "$DEMO_TMP/tool-prices.json" --max-cost 5
demo_wait "$GATE2"
for i in 1 2 3; do
  printf 'call %d  ' "$i"
  anthropic_call "$GATE2" priced
done
echo "Priced, the per-request charge is counted toward --max-cost and no boundary trips."
