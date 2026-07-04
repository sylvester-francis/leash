#!/usr/bin/env bash
# Tier 1, the default: wrap an agent under a budget. leash launches the child
# with OPENAI_BASE_URL pointed at its embedded proxy, meters every call, and the
# child's next call is refused once the budget trips. leash exits 3 when a
# boundary stopped the run.
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 4000 --completion-tokens 2000
demo_wait "$UPSTREAM"

# A minimal "agent": call the injected base URL until a call is refused. A real
# agent would use an OpenAI/Anthropic SDK, which reads OPENAI_BASE_URL the same way.
cat > "$DEMO_TMP/agent.sh" <<'AGENT'
#!/usr/bin/env bash
set -euo pipefail
for i in $(seq 1 10); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "$OPENAI_BASE_URL/chat/completions" -d '{"model":"demo-model"}')
  echo "  agent: call $i -> HTTP $code"
  [ "$code" = "200" ] || { echo "  agent: call refused, exiting"; break; }
done
AGENT

demo_banner "Wrap an agent under a \$0.25 budget (each call \$0.10)"
demo_run "leash --max-cost 0.25 --prices examples/prices.demo.json --upstream http://$UPSTREAM -- bash agent.sh"
set +e
"$LEASH" --max-cost 0.25 --prices "$PRICES" --upstream "http://$UPSTREAM" \
  --db "$DB" --run wrap-demo --log-level error -- bash "$DEMO_TMP/agent.sh"
echo "leash exit code: $?  (3 = a boundary stopped the run)"
