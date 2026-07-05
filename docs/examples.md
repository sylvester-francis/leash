# Examples and recipes

Copy-paste commands for common tasks, grouped by what you want to do. They target
real providers; swap in your own model names, prices, and budgets. For demos you
can run right now with no API key, see the runnable scripts in
[`examples/`](../examples/README.md).

Every governance flag also reads a `LEASH_`-prefixed environment variable, and
`serve` requires a token by default (pass `--insecure` for a trusted local
socket). Full flag detail is in [cli-reference.md](cli-reference.md).

## Wrap an agent (Tier 1)

Launch a process under a budget; leash points its SDK at an embedded proxy.

```sh
# A $5 budget and a 15-minute deadline around a Python agent.
leash --max-cost 5 --deadline 15m --prices prices.json -- python agent.py

# No price table: cap calls, token rate, and repetition instead.
leash --max-calls 500 --rate 200000/1m --stall 4 -- ./agent.sh

# Resume the same budget across runs by reusing a run id.
leash --max-cost 20 --prices prices.json --run nightly-batch -- python agent.py
```

Exit code `3` means a boundary stopped the run; otherwise leash returns the
child's own exit code.

## Run the gateway (Tier 2)

One proxy in front of many agents; each tags its run with `X-Loop-Id`.

```sh
export LEASH_AUTH_TOKEN=$(leash gen-token)
leash serve --listen :8088 --max-cost 20 --prices prices.json --require-run-id

# a client, any language:
curl http://localhost:8088/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "X-Leash-Token: $LEASH_AUTH_TOKEN" \
  -H "X-Loop-Id: nightly-batch-7" \
  -d '{"model":"gpt-4o", ...}'
```

Point an SDK at it by setting its base URL to `http://localhost:8088/v1`
(OpenAI) or `http://localhost:8088` (Anthropic).

## Bound cost precisely

```sh
# Total budget, plus a cap on any single call (stops the run if one exceeds it).
leash serve --max-cost 50 --max-cost-per-call 2.00 --prices prices.json

# Add a compute meter (dollars per hour of wall-clock) on top of token cost.
leash serve --max-cost 50 --compute-rate 1.20 --prices prices.json
```

Prices are per million tokens, with optional cache rates:

```json
{
  "gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0, "cached_input": 1.25},
  "o1":     {"input": 15,  "output": 60, "reasoning": 60}
}
```

Optional per-model fields refine the price when you need them, each falling back
to a coarser rate: audio, per-TTL cache-write, per-request tool charges, and
per-service-tier overrides. Each subset is priced exactly once.

```json
{
  "claude-sonnet": {
    "input": 3, "output": 15, "reasoning": 15,
    "cache_write_5m": 3.75, "cache_write_1h": 6,
    "web_search_per_request": 0.01,
    "tiers": {"batch": {"input": 1.5, "output": 7.5}}
  }
}
```

See `examples/demos/10-server-tools.sh` for the tool-pricing behavior end to end.

## Handle calls leash cannot price

```sh
# Default: refuse a call that can't be metered under a cost budget (fail closed).
leash serve --max-cost 20 --prices prices.json               # --on-blind=refuse

# Only warn and keep going (the pre-v0.2 behavior).
leash serve --max-cost 20 --prices prices.json --on-blind=warn
```

"Cannot price" covers billed activity that is not tokens, too: a call that reports
a server-side tool request (Anthropic `server_tool_use` web search / fetch) with
no `web_search_per_request` / `web_fetch_per_request` rate fails closed the same
way (reason `server_tool_unpriced`), until you price the tool or set
`--on-blind=warn`.

## Govern Gemini, Ollama, or any OpenAI-compatible endpoint

leash keys on the wire format, not the model name, so it governs far more than
OpenAI and Anthropic. Point `--upstream` at the endpoint and supply prices:

```sh
# Gemini's native generateContent API (no default upstream, so set it).
leash serve --max-cost 20 --prices gemini-prices.json \
  --upstream https://generativelanguage.googleapis.com

# Any OpenAI-compatible endpoint (Gemini's OpenAI-compat URL, OpenRouter, Groq, ...).
leash serve --max-cost 20 --prices prices.json --upstream https://openrouter.ai/api

# A local model via Ollama: tokens are effectively free, so govern by machine time.
leash serve --compute-rate 1.50 --deadline 30m --upstream http://localhost:11434
```

`examples/demos/11-gemini.sh` runs the native-Gemini path end to end offline.

## Get warned before the cliff

Budgets are a hard stop; `--warn-at` gives you an early signal, and `--webhook`
pushes it somewhere.

```sh
# Warn at 90% instead of the default 80%; 0 turns warnings off.
leash serve --max-cost 50 --prices prices.json --warn-at 0.90

# POST a JSON event to your incident tool on approach and on stop.
leash serve --max-cost 50 --prices prices.json --webhook https://hooks.example.com/leash
```

Warnings also increment `leash_budget_warnings_total{reason}` for alerting. The
rate limit is the one soft boundary: a refused call returns `Retry-After` and the
run resumes once its window decays, so a client can back off and retry.

## Escalate durably when a run stops

`--webhook` is best-effort. Add `--reactions-db` (a store separate from `--db`)
to deliver the reaction as a crash-surviving, retried workflow off the
enforcement path, and `--on-event-exec` to run a local command hook with the
event in `LEASH_*` environment variables:

```sh
leash serve --max-cost 50 --prices prices.json \
  --webhook https://hooks.example.com/leash \
  --reactions-db /var/lib/leash/reactions.db \
  --on-event-exec /etc/leash/on-event.sh
```

Delivery is at-least-once and deduplicated per run, so a retry never
double-sends. leash ships no connectors; the command hook is how you reach one
(see [`examples/hooks/on-event.sh`](../examples/hooks/on-event.sh)).

## Isolate tenants

With auth on, a run id is scoped to the presenting token, so two callers using
the same `X-Loop-Id` get separate budgets. Configure two tokens to rotate with
no downtime:

```sh
leash serve --auth-token "$OLD_TOKEN $NEW_TOKEN" --max-cost 20 --prices prices.json
# or keep tokens off the process list entirely:
leash serve --auth-token-file /etc/leash/tokens --max-cost 20 --prices prices.json
```

## Inspect and control runs

```sh
leash ps --db ./team.db --prices prices.json                 # list active runs
leash inspect nightly-batch-7 --db ./team.db --prices prices.json   # one run's journal
leash kill nightly-batch-7 --db ./team.db                    # stop it on its next call
```

Add `--json` to `ps` and `inspect` for machine-readable output.

## Observe it

```sh
leash serve --admin :9090 --max-cost 20 --prices prices.json

curl localhost:9090/healthz            # liveness (always 200)
curl localhost:9090/readyz             # readiness (ledger write probe)
curl -H "X-Leash-Token: $TOKEN" localhost:9090/metrics    # Prometheus text
leash healthcheck --url http://localhost:9090/healthz     # for a container HEALTHCHECK
```

## Durable, shared, and highly available

```sh
# Point at Postgres for a cross-process lease (active/passive HA).
export LEASH_AUTH_TOKEN=$(cat /etc/leash/token)
leash serve --db postgres://user:pass@host/leash --max-cost 20 --prices prices.json

# A warm standby on the same ledger takes over when the primary steps down.
leash serve --db postgres://user:pass@host/leash --max-cost 20 --prices prices.json --standby
```

See [durability.md](durability.md) for the ledger and failover model, and
[deployment.md](deployment.md) for systemd, Docker, and Kubernetes.
