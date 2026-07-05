# Examples

Runnable, offline demos of leash. Each script builds `leash` and a standard-
library `fakeupstream` into a temp directory, drives a few calls, and shows a
boundary tripping - no API key, no real spend, nothing to clean up.

## Running

You need the Go toolchain and `curl`. From the repository root:

```sh
bash examples/demos/02-gateway-cost-budget.sh
```

Each script is self-contained: it picks uncommon ports, refuses to start if one
is busy, and tears down its servers on exit.

## The demos

| Script | Shows |
|---|---|
| `demos/01-wrap-agent.sh` | Tier 1 - wrap an agent under a cost budget; the child's next call is refused when it trips (leash exits 3). |
| `demos/02-gateway-cost-budget.sh` | Tier 2 - a gateway with `--max-cost`, the `429` boundary body, and the one-call overshoot. |
| `demos/03-calls-and-stall.sh` | `--max-calls` and `--stall` - the boundaries that need no price table. |
| `demos/04-tenancy.sh` | Per-credential isolation: two tokens with the same `X-Loop-Id` get separate budgets. |
| `demos/05-inspect-and-kill.sh` | `leash ps`, `leash inspect`, and `leash kill` against the durable ledger. |
| `demos/06-per-call-cap-and-blind.sh` | `--max-cost-per-call`, and fail-closed metering (`--on-blind=refuse` vs `warn`). |
| `demos/07-admin-and-metrics.sh` | `/healthz`, `/readyz`, `leash healthcheck`, and the Prometheus `/metrics`. |
| `demos/08-soft-limits.sh` | `--warn-at` early warning, and the rate limit as recoverable backpressure (`Retry-After`). |
| `demos/09-durable-reactions.sh` | `--reactions-db` + `--on-event-exec` - a crash-surviving reaction (command hook) fires off the hot path when a run stops. |

`demos/smoke.sh` is not a showcase - it is the same harness driven as an
asserting end-to-end test, run by CI to catch regressions in the whole path
(meter, ledger, boundary body, admin surface).

For copy-paste recipes you can adapt to real providers, see
[`docs/examples.md`](../docs/examples.md).

## fakeupstream

`fakeupstream` is a tiny OpenAI-compatible stand-in used by the demos and the
`docker compose` quickstart. Flags let a demo shape what it reports:

```
--model, --reply                       identity and content
--prompt-tokens, --completion-tokens   the usage counts (drive cost)
--reasoning-tokens, --cached-tokens    detail fields (reasoning, prompt cache)
--no-usage                             omit usage entirely (the blind path)
```

It is a demo aid, not part of the leash product.

## hooks

[`hooks/on-event.sh`](hooks/on-event.sh) is a reference command hook for
`leash serve --on-event-exec` (durable reactions). It shows the `LEASH_*`
environment leash hands a reaction and posts to Slack when `SLACK_WEBHOOK_URL`
is set. Copy it and make it yours; leash ships no built-in integrations.
