#!/usr/bin/env bash
# Native Gemini metering. leash reads Google's generateContent usageMetadata
# (promptTokenCount / candidatesTokenCount / thoughtsTokenCount / cached) and
# enforces a cost budget on it, exactly as it does for OpenAI and Anthropic. On
# the Gemini API candidatesTokenCount already includes the thinking tokens, so
# they are priced once. Gemini has no default upstream, so --upstream is set.
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"
demo_require_free "$GATE"
# ~$0.10/call: input 4000 @ $10/M = $0.04; output 2000 (incl 500 thoughts) @ $30/M = $0.06.
demo_bg "$FAKEUP" --listen "$UPSTREAM" --model gemini-demo \
  --prompt-tokens 4000 --completion-tokens 2000 --reasoning-tokens 500
demo_wait "$UPSTREAM"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"
echo '{"gemini-demo": {"input": 10, "output": 30, "reasoning": 30}}' >"$DEMO_TMP/gemini-prices.json"

demo_banner "Govern a native Gemini generateContent endpoint under a \$0.25 budget"
demo_run "leash serve --max-cost 0.25 --prices gemini-prices.json --upstream http://gemini-host"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$DEMO_TMP/gemini-prices.json" --max-cost 0.25
demo_wait "$GATE"
for i in 1 2 3 4; do
  printf 'call %d  ' "$i"
  curl -s -o "$DEMO_TMP/resp" -w 'HTTP %{http_code}  ' -X POST \
    "http://$GATE/v1beta/models/gemini-demo:generateContent" \
    -H "X-Leash-Token: $LEASH_AUTH_TOKEN" -H "X-Loop-Id: gem" -d '{"contents":[]}'
  grep -oE '"reason":"[a-z_]+"|"content"' "$DEMO_TMP/resp" | head -1
done
echo
echo "leash metered Gemini's usageMetadata and stopped on the cost budget - the"
echo "thinking tokens (thoughtsTokenCount) were priced once as a subset of output."
