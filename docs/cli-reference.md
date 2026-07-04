# CLI reference

Every command, flag, and exit code. leash uses only the standard library flag
package: flags take `--name value` or `--name=value`, and in wrapper mode `--`
separates leash's flags from the child command.

## Commands

```
leash [flags] -- <command> [args...]   wrap an agent (Tier 1, the default)
leash serve [flags]                    standalone gateway (Tier 2)
leash ps [flags] [--json]              list active runs from the ledger
leash inspect [flags] [--json] <run>   show one run's folded journal
leash kill [flags] <run>               durably stop a run on its next call
leash version                          print the build version
leash gen-token                        print a random token for --auth-token
leash help                             top-level help
```

`leash version` prints one line, `leash <version> <goversion> <os>/<arch>`, with
the version stamped at release time.

`run` is also accepted as an explicit subcommand (`leash run [flags] -- ...`),
but it is the default: anything that is not another subcommand is treated as a
wrap invocation.

## Governance flags

Shared by `run` and `serve`. A zero value disables the corresponding boundary;
the kill switch is always active.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--max-cost` | dollars | `5.00` | budget over token + compute cost; `0` disables |
| `--max-calls` | int | `100` | maximum governed calls; `0` disables |
| `--deadline` | duration | `30m` | wall-clock budget from the first call; `0` disables |
| `--rate` | tokens/window | off | trailing token rate, e.g. `100000/1m`; empty disables |
| `--stall` | int | off | identical responses tolerated in a row; `0` disables |
| `--prices` | path | none | JSON price table; without it the token meter is blind |
| `--compute-rate` | dollars/hour | `0` | compute meter rate |
| `--upstream` | URL | inferred | upstream base URL override; empty infers per provider |
| `--db` | path | `~/.leash/leash.db` | ledger database path |
| `--run` | name | random | run name; reuse it to resume that budget |
| `--no-inject` | bool | `false` | do not add `stream_options.include_usage` |
| `--max-body-bytes` | int | `10485760` | cap on the request body in bytes (10 MiB); over-cap gets 413 |
| `--upstream-header-timeout` | duration | `5m` | how long the upstream may take to send response headers; `0` disables; the body stream is never capped |
| `--log-level` | string | `info` | `debug`, `info`, `warn`, or `error` |
| `--log-format` | string | `text` | `text` or `json` |

A run id (the `--run` flag and the `X-Loop-Id` header) must match
`^[A-Za-z0-9][A-Za-z0-9._-]{0,117}$`. An invalid `--run` exits 1; an invalid
header gets 400.

`serve` adds:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--listen` | address | `:8088` | address the gateway listens on |
| `--require-run-id` | bool | `false` | refuse a request with no `X-Loop-Id` (400) instead of pooling it into `default` |
| `--admin` | address | off | admin listener for `/healthz`, `/readyz`, `/metrics`; empty disables |
| `--standby` | bool | `false` | wait for the governance lease instead of erroring when another instance holds it (active/passive HA) |
| `--auth-token` | string | off | require a matching `X-Leash-Token` header; space-separate two for zero-downtime rotation; prefer `LEASH_AUTH_TOKEN` |
| `--max-runs` | int | `0` | cap on runs tracked in memory at once; a new run beyond it is refused 503 (0 disables) |

`ps` and `inspect` accept the governance flags too, so `--db`, `--prices`, and
`--compute-rate` let them compute and display costs, and both take `--json`.
`kill` accepts only `--db`.

The `--rate` value is `tokens/window`: an integer, a slash, and a Go duration
(`100000/1m`, `50000/30s`, `2000000/1h`). Both parts are required.

The `--deadline` value is a Go duration (`30m`, `15m`, `1h30m`, `90s`).

## The price table

`--prices` points at a JSON object mapping model name to dollars per million
input, output, and reasoning tokens:

```json
{
  "gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0},
  "o1": {"input": 15, "output": 60, "reasoning": 60}
}
```

An unknown model or an absent table means that call's token cost is zero. See
[cost-model.md](cost-model.md).

## Environment variables leash sets (wrapper mode)

In Tier 1, leash injects these into the child so its SDK sends traffic through
the embedded proxy:

```
OPENAI_BASE_URL   = http://127.0.0.1:PORT/v1
OPENAI_API_BASE   = http://127.0.0.1:PORT/v1     (legacy alias)
ANTHROPIC_BASE_URL= http://127.0.0.1:PORT        (SDK appends /v1/messages)
```

## Environment variables leash reads

Every shared flag also reads a `LEASH_`-prefixed variable named mechanically from
the flag: `--max-cost` reads `LEASH_MAX_COST`, `--db` reads `LEASH_DB`, `--admin`
reads `LEASH_ADMIN`, and so on for every flag in the tables above. Precedence is
explicit flag, then environment, then the built-in default. A malformed value is
reported on stderr and the default is used. No YAML or config files: flags and
`LEASH_*` variables only. See docs/deployment.md for the full table.

## The run header (gateway mode)

In Tier 2, a client tags each run with a header so it gets its own durable
budget:

```
X-Loop-Id: <run>
```

Without the header, a call falls under the run id `default`. The header is
consumed by leash and never forwarded upstream.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success (a clean child exit in wrapper mode, or a clean server shutdown) |
| `3` | a boundary stopped the run (wrapper mode) |
| `2` | usage error (bad flags, missing command or run id) |
| `1` | runtime error (for example the ledger could not be opened) |
| other | in wrapper mode, the child's own non-zero exit code when no boundary fired |

## The 429 boundary body

A refused call returns HTTP 429 with:

```json
{"error":{"type":"leash_boundary","reason":"cost_budget","run":"a3f9",
  "calls":18,"token_cost":4.10,"compute_cost":0.91,"total_cost":5.01}}
```

`reason` is one of `kill_switch`, `deadline`, `cost_budget`, `max_calls`,
`rate_limit`, `stall`. Every later call for a stopped run returns the same body.

## The stop line

In wrapper mode leash prints one line to stderr when the child exits. A boundary
stop:

```
leash: stopped run a3f9 after 18 calls, $4.10 tokens + $0.91 compute = $5.01 (cost_budget)
```

A clean finish:

```
leash: run a3f9 finished after 12 calls, $2.30 tokens + $0.00 compute = $2.30 (child_exited)
```

## JSON output

`--json` on `ps` and `inspect` emits stable machine-readable shapes; the human
tables stay the default. `ps --json` is an array of run summaries:

```json
[
  {"run":"nightly-7","calls":18,"input_tokens":36000,"output_tokens":12000,
   "reasoning_tokens":0,"token_cost":4.10,"compute_cost":0.91,"total_cost":5.01,
   "status":"stopped","reason":"cost_budget"}
]
```

`inspect --json` is one object: the same run summary fields plus an `entries`
array of the decoded journal:

```json
{"run":"nightly-7","calls":18,"input_tokens":36000,"output_tokens":12000,
 "reasoning_tokens":0,"token_cost":4.10,"compute_cost":0.91,"total_cost":5.01,
 "status":"stopped","reason":"cost_budget",
 "entries":[
   {"seq":0,"tag":"call-0","at":"2026-07-03T15:08:05-04:00","kind":"call",
    "model":"gpt-4o","input_tokens":2000,"output_tokens":650,"reasoning_tokens":0},
   {"seq":18,"tag":"stop","at":"2026-07-03T15:12:41-04:00","kind":"stop",
    "reason":"cost_budget"}
 ]}
```

`status` is `running`, `killed`, or `stopped`; `reason` is empty until a boundary
stops the run.

## Color

`ps`, `inspect`, and the stop line color the run status when standard output (or
stderr, for the stop line) is a terminal: running is green, stopped is amber,
killed is red. Color is off for pipes, redirects, and `--json`, and honors the
`NO_COLOR` environment variable.

## The admin listener

When `serve --admin ADDR` is set, a second HTTP server on `ADDR` serves:

- `GET /healthz` - liveness, always `200 ok`.
- `GET /readyz` - `200 ready` when a ledger read succeeds within 1s, else `503`.
- `GET /metrics` - Prometheus text exposition (`text/plain; version=0.0.4`).

The metrics carry no run-id labels. Counters: `leash_calls_total{decision,
provider}`, `leash_stops_total{reason}`, `leash_tokens_total{kind}`,
`leash_token_cost_usd_total`, `leash_blind_calls_total`,
`leash_upstream_errors_total`. Gauges: `leash_build_info{version}`,
`leash_active_runs`. See docs/deployment.md and docs/operations.md.

## Examples

```sh
# Wrap a Python agent under a $5 budget and a 15-minute deadline.
leash --max-cost 5 --deadline 15m --prices prices.json -- python agent.py

# Rate-limit and stall-guard a shell agent, no cost meter.
leash --max-calls 500 --rate 200000/1m --stall 4 -- ./agent.sh

# Run the gateway on all interfaces, port 8088.
leash serve --listen 0.0.0.0:8088 --max-cost 20 --prices prices.json

# Inspect and stop, from another terminal.
leash ps --db ./team.db --prices prices.json
leash inspect nightly-42 --db ./team.db --prices prices.json
leash kill nightly-42 --db ./team.db
```
