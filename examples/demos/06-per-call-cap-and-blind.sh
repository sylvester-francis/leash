#!/usr/bin/env bash
# Two cost-integrity features:
#   1. --max-cost-per-call caps a single call and stops the run if one exceeds it.
#   2. Fail-closed metering: under a cost budget, a call leash cannot price stops
#      the run by default (--on-blind=refuse); --on-blind=warn lets it pass.
source "$(dirname "$0")/_lib.sh"

BIG=127.0.0.1:19099      # upstream that reports a large, expensive call
BLIND=127.0.0.1:19100    # upstream that reports NO usage (unmeterable)
REFUSE=127.0.0.1:18088
WARN=127.0.0.1:18089

demo_build
demo_require_free "$BIG"; demo_require_free "$BLIND"; demo_require_free "$REFUSE"; demo_require_free "$WARN"
demo_bg "$FAKEUP" --listen "$BIG" --prompt-tokens 20000 --completion-tokens 10000  # $0.50/call
demo_bg "$FAKEUP" --listen "$BLIND" --no-usage
demo_wait "$BIG"; demo_wait "$BLIND"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"

demo_banner "--max-cost-per-call 0.25: one \$0.50 call stops the run"
demo_bg "$LEASH" serve --listen "$REFUSE" --upstream "http://$BIG" \
  --db "$DEMO_TMP/big.db" --prices "$PRICES" --max-cost 100 --max-cost-per-call 0.25
demo_wait "$REFUSE"
for i in 1 2; do
  printf 'call %d  ' "$i"
  demo_post "http://$REFUSE/v1/chat/completions" "$LEASH_AUTH_TOKEN" pc '{"model":"demo-model"}'
done

demo_banner "Blind + cost budget, default --on-blind=refuse: the run stops"
demo_bg "$LEASH" serve --listen "$WARN" --upstream "http://$BLIND" \
  --db "$DEMO_TMP/blind1.db" --prices "$PRICES" --max-cost 5
demo_wait "$WARN"
for i in 1 2; do
  printf 'call %d  ' "$i"
  demo_post "http://$WARN/v1/chat/completions" "$LEASH_AUTH_TOKEN" b '{"model":"demo-model"}'
done

demo_banner "Same blind upstream, --on-blind=warn: calls pass (warned, not stopped)"
BLIND2=127.0.0.1:18090
demo_require_free "$BLIND2"
demo_bg "$LEASH" serve --listen "$BLIND2" --upstream "http://$BLIND" \
  --db "$DEMO_TMP/blind2.db" --prices "$PRICES" --max-cost 5 --on-blind warn
demo_wait "$BLIND2"
for i in 1 2 3; do
  printf 'call %d  ' "$i"
  demo_post "http://$BLIND2/v1/chat/completions" "$LEASH_AUTH_TOKEN" w '{"model":"demo-model"}'
done
