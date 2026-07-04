#!/usr/bin/env bash
# The ledger is readable and controllable from any terminal. Make a few calls,
# then list runs (ps), read one run's folded journal (inspect), and stop a run
# (kill) - the kill takes effect on the run's next call. Runs --insecure so the
# run id stays plain ("nightly") for ps/inspect/kill; a real gateway sets a token.
source "$(dirname "$0")/_lib.sh"

demo_build
demo_require_free "$UPSTREAM"; demo_require_free "$GATE"
demo_bg "$FAKEUP" --listen "$UPSTREAM" --prompt-tokens 2000 --completion-tokens 1000
demo_wait "$UPSTREAM"
demo_bg "$LEASH" serve --listen "$GATE" --upstream "http://$UPSTREAM" \
  --db "$DB" --prices "$PRICES" --max-cost 100 --insecure
demo_wait "$GATE"

demo_banner "Make three calls on run \"nightly\""
for i in 1 2 3; do
  printf 'call %d  ' "$i"
  demo_post "http://$GATE/v1/chat/completions" "" nightly '{"model":"demo-model"}'
done

demo_banner "leash ps - list active runs"
demo_run "leash ps --db leash.db --prices examples/prices.demo.json"
"$LEASH" ps --db "$DB" --prices "$PRICES"

demo_banner "leash inspect nightly - one run's folded journal"
demo_run "leash inspect nightly --db leash.db --prices examples/prices.demo.json"
"$LEASH" inspect nightly --db "$DB" --prices "$PRICES"

demo_banner "leash kill nightly - stop it on the next call"
demo_run "leash kill nightly --db leash.db"
"$LEASH" kill nightly --db "$DB"
printf 'next call  '
demo_post "http://$GATE/v1/chat/completions" "" nightly '{"model":"demo-model"}'
