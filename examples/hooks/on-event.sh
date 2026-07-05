#!/bin/sh
# Example leash command hook, for `leash serve --on-event-exec /path/to/on-event.sh`
# (requires --reactions-db). leash runs this on a stop or a budget warning, off
# the enforcement path, with the event in these environment variables:
#
#   LEASH_EVENT       "stopped" or "warning"
#   LEASH_RUN         the run id
#   LEASH_REASON      the boundary or budget (e.g. cost_budget, deadline)
#   LEASH_CALLS       number of model calls made
#   LEASH_TOTAL_COST  dollars spent
#
# leash runs the command directly, with no shell, so put logic here rather than
# arguments in the flag. Delivery is durable and at-least-once, so keep this
# idempotent. This example logs a line and, if SLACK_WEBHOOK_URL is set in the
# gateway's environment, posts to Slack.
set -eu

line="leash ${LEASH_EVENT}: run ${LEASH_RUN} (${LEASH_REASON}) - ${LEASH_CALLS} calls, \$${LEASH_TOTAL_COST}"
echo "$line"

if [ -n "${SLACK_WEBHOOK_URL:-}" ]; then
  curl -sS -X POST "$SLACK_WEBHOOK_URL" \
    -H 'Content-Type: application/json' \
    -d "{\"text\": \"$line\"}" >/dev/null
fi
