#!/usr/bin/env bash
# The admin listener: liveness (/healthz), readiness (/readyz, a ledger write
# probe), and Prometheus /metrics on a separate address. `leash healthcheck`
# probes it with no shell or curl - the container HEALTHCHECK uses it.
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$GATE"; demo_require_free "$ADMIN"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 3000 --completion-tokens 1500
demo_wait "$UPSTREAM"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"
demo_bg "$LEASH" serve --listen "$GATE" --admin "$ADMIN" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 100
demo_wait "$GATE"; demo_wait "$ADMIN"

# Drive a little traffic so the metrics are non-zero.
for i in 1 2 3; do
  demo_post "http://$GATE/v1/chat/completions" "$LEASH_AUTH_TOKEN" metrics '{"model":"demo-model"}' >/dev/null
done

demo_banner "Liveness and readiness (open, no token needed)"
demo_run "curl $ADMIN/healthz"; curl -s "http://$ADMIN/healthz"
demo_run "curl $ADMIN/readyz";  curl -s "http://$ADMIN/readyz"

demo_banner "leash healthcheck - the container HEALTHCHECK probe"
demo_run "leash healthcheck --url http://$ADMIN/healthz"
"$LEASH" healthcheck --url "http://$ADMIN/healthz" && echo "exit 0 (healthy)"

demo_banner "A few Prometheus metrics (/metrics wants the token when auth is on)"
demo_run "curl -H 'X-Leash-Token: \$TOKEN' $ADMIN/metrics | grep leash_"
curl -s -H "X-Leash-Token: $LEASH_AUTH_TOKEN" "http://$ADMIN/metrics" \
  | grep -E '^leash_(calls_total|tokens_total|token_cost_usd_total|active_runs|ledger_errors_total)' | head
