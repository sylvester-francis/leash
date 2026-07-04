#!/usr/bin/env bash
# Boundaries that need no cost meter: --max-calls (a hard call count) and --stall
# (stop when the model repeats itself). Useful when you have no price table.
source "$(dirname "$0")/_lib.sh"

A=127.0.0.1:18088
B=127.0.0.1:18089

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$A"; demo_require_free "$B"
demo_bg "$FAKEUP" --listen "$UPSTREAM"   # returns the same "ok" reply every call
demo_wait "$UPSTREAM"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"

demo_banner "--max-calls 3: stop after three calls"
demo_bg "$LEASH" serve --listen "$A" --upstream "http://$UPSTREAM" \
  --db "$DEMO_TMP/a.db" --max-calls 3 --max-cost 0
demo_wait "$A"
for i in 1 2 3 4; do
  printf 'call %d  ' "$i"
  demo_post "http://$A/v1/chat/completions" "$LEASH_AUTH_TOKEN" calls '{"model":"demo-model"}'
done

demo_banner "--stall 3: stop when replies repeat (the upstream keeps saying \"ok\")"
demo_bg "$LEASH" serve --listen "$B" --upstream "http://$UPSTREAM" \
  --db "$DEMO_TMP/b.db" --stall 3 --max-cost 0 --max-calls 0
demo_wait "$B"
for i in 1 2 3 4 5; do
  printf 'call %d  ' "$i"
  demo_post "http://$B/v1/chat/completions" "$LEASH_AUTH_TOKEN" stall '{"model":"demo-model"}'
done
