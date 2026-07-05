#!/usr/bin/env bash
# Durable reactions: when a run stops (or approaches a budget), leash runs a
# crash-surviving reaction off the enforcement path - a webhook and/or a command
# hook, retried until they land. Here we use the command hook, so the demo needs
# nothing but a shell script; --webhook delivers the same event, durably. The
# reactions store is separate from the ledger (--reactions-db, distinct from --db).
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"
demo_require_free "$GATE"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 4000 --completion-tokens 2000 # ~$0.10/call
demo_wait "$UPSTREAM"
export LEASH_AUTH_TOKEN="$("$LEASH" gen-token)"

# The command hook: a script that appends the event handed to it in LEASH_* env.
HOOK="$DEMO_TMP/on-event.sh"
HOOK_LOG="$DEMO_TMP/reactions.log"
cat >"$HOOK" <<EOF
#!/bin/sh
printf '%s run=%s reason=%s cost=%s\n' "\$LEASH_EVENT" "\$LEASH_RUN" "\$LEASH_REASON" "\$LEASH_TOTAL_COST" >> "$HOOK_LOG"
EOF
chmod +x "$HOOK"

demo_banner "Serve with a durable reaction on stop (reactions store separate from the ledger)"
demo_run "leash serve --max-cost 0.25 --reactions-db reactions.db --on-event-exec on-event.sh"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 0.25 \
  --reactions-db "$DEMO_TMP/reactions.db" --on-event-exec "$HOOK"
demo_wait "$GATE"

echo "# spend past the \$0.25 budget so the run stops:"
for i in 1 2 3 4; do
  printf 'call %d  ' "$i"
  demo_post "http://$GATE/v1/chat/completions" "$LEASH_AUTH_TOKEN" nightly '{"model":"demo-model"}'
done

echo
echo "--- the reaction fired off the hot path; the command hook recorded the stop:"
for _ in $(seq 1 50); do
  [ -s "$HOOK_LOG" ] && break
  sleep 0.1
done
cat "$HOOK_LOG" 2>/dev/null || echo "(no reaction recorded yet)"

echo
echo "The reaction ran after the stop, not on the request path. It is retried and,"
echo "because it lives in the reactions store, resumes after a restart; --webhook"
echo "delivers the same event durably. leash ships no connectors - the hook is how"
echo "you reach yours."
