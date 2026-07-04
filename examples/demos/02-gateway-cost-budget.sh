#!/usr/bin/env bash
# Tier 2, cost budget: run leash as a gateway with a $0.25 budget. Each call
# costs $0.10 (4000 input @ $10/M + 2000 output @ $30/M), so the budget trips.
# Note the one-call overshoot: the call that crosses the budget still completes.
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$GATE"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 4000 --completion-tokens 2000
demo_wait "$UPSTREAM"

export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 0.25
demo_wait "$GATE"

demo_banner "Gateway under a \$0.25 cost budget (each call \$0.10)"
demo_run "curl -H 'X-Leash-Token: \$TOKEN' -H 'X-Loop-Id: demo' -d '{\"model\":\"demo-model\"}' $GATE/v1/chat/completions"
for i in 1 2 3 4 5; do
  printf 'call %d  ' "$i"
  demo_post "http://$GATE/v1/chat/completions" "$LEASH_AUTH_TOKEN" demo '{"model":"demo-model"}'
done

echo
echo "The run stopped on the cost budget; every later call returns the same 429 body."
