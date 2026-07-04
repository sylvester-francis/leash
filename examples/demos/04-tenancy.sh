#!/usr/bin/env bash
# Tenant isolation: with auth on, a run id is scoped to the presenting token, so
# two tenants using the SAME X-Loop-Id get separate budgets. Neither can burn or
# read the other's run. Each tenant here gets one call (--max-calls 1).
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$GATE"
demo_bg "$FAKEUP" --listen "$UPSTREAM"
demo_wait "$UPSTREAM"

TOKEN_A="$("$LEASH" gen-token)"
TOKEN_B="$("$LEASH" gen-token)"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --auth-token "$TOKEN_A $TOKEN_B" --max-calls 1 --max-cost 0
demo_wait "$GATE"

demo_banner "Two tenants, one shared run id, isolated budgets"
printf 'tenant A, run "shared"  '
demo_post "http://$GATE/v1/chat/completions" "$TOKEN_A" shared '{"model":"demo-model"}'
printf 'tenant A, run "shared"  '
demo_post "http://$GATE/v1/chat/completions" "$TOKEN_A" shared '{"model":"demo-model"}'
printf 'tenant B, run "shared"  '
demo_post "http://$GATE/v1/chat/completions" "$TOKEN_B" shared '{"model":"demo-model"}'

echo
echo "Tenant A is exhausted after one call; tenant B, using the same run id, is untouched."
