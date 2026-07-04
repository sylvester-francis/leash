#!/usr/bin/env bash
# Soft limits: an early warning before the hard stop (--warn-at), and the rate
# limit as recoverable backpressure (Retry-After, then resume) rather than a
# terminal stop. Point --webhook at a URL to also push these as JSON events.
source "$(dirname "$0")/_lib.sh"

RATE=127.0.0.1:18089

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$GATE"; demo_require_free "$RATE"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 4000 --completion-tokens 2000  # $0.10/call, 6000 tokens
demo_wait "$UPSTREAM"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"

demo_banner "--warn-at 0.5: a warning at 50% of a \$0.25 budget, before it stops"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 0.25 --warn-at 0.5
demo_wait "$GATE"
for i in 1 2 3 4; do
  printf 'call %d  ' "$i"
  demo_post "http://$GATE/v1/chat/completions" "$LEASH_AUTH_TOKEN" warn '{"model":"demo-model"}'
done
echo "--- the warning leash logged (also a leash_budget_warnings_total metric, and --webhook if set):"
grep 'approaching budget' "$DEMO_TMP/bg.log" | tail -1 || echo "(no warning line captured)"

demo_banner "Rate limit as backpressure: Retry-After, then recovery"
demo_bg "$LEASH" serve --listen "$RATE" --upstream "http://$UPSTREAM" \
  --db "$DEMO_TMP/rate.db" --rate 5000/2s --max-cost 0
demo_wait "$RATE"
printf 'call 1  '; demo_post "http://$RATE/v1/chat/completions" "$LEASH_AUTH_TOKEN" rl '{"model":"demo-model"}'
printf 'call 2 headers  '
curl -s -D - -o /dev/null -X POST "http://$RATE/v1/chat/completions" \
  -H "X-Leash-Token: $LEASH_AUTH_TOKEN" -H "X-Loop-Id: rl" -d '{"model":"demo-model"}' \
  | grep -iE '^HTTP|^retry-after' | tr -d '\r'
echo "waiting 2s for the trailing window to decay ..."
sleep 2.2
printf 'after window  '; demo_post "http://$RATE/v1/chat/completions" "$LEASH_AUTH_TOKEN" rl '{"model":"demo-model"}'
echo
echo "The run was never stopped - it resumed once the rate window decayed."
