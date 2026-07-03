# CLI reference

Every command, flag, and exit code. leash uses only the standard library flag
package: flags take `--name value` or `--name=value`, and in wrapper mode `--`
separates leash's flags from the child command.

## Commands

```
leash [flags] -- <command> [args...]   wrap an agent (Tier 1, the default)
leash serve [flags]                    standalone gateway (Tier 2)
leash ps [flags]                       list active runs from the ledger
leash inspect [flags] <run>            show one run's folded journal
leash kill [flags] <run>               durably stop a run on its next call
leash help                             top-level help
```

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

`serve` adds:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--listen` | address | `:8088` | address the gateway listens on |

`ps` and `inspect` accept the governance flags too, so `--db`, `--prices`, and
`--compute-rate` let them compute and display costs. `kill` accepts only `--db`.

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

leash reads no environment variables of its own; all configuration is via flags.

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
