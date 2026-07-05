# FAQ

Honest answers for a skeptical engineer. Everything here matches what leash
does today; where it does not do something, this says so.

## Does leash need me to change my agent's code?

In Tier 1 (wrap), no. `leash -- python my_agent.py` starts an embedded proxy on
a free port and sets the base-url environment variables your SDK already reads,
so the SDK sends its calls through leash with no line of code changing:

```sh
leash -- python my_agent.py
```

In Tier 2 (serve), you point the agent's `base_url` at the leash address and add
one header, `X-Loop-Id`, so each run gets its own durable budget:

```sh
LEASH_AUTH_TOKEN=$(leash gen-token) leash serve --listen :8088 --max-cost 5.00 --prices prices.json
```

`serve` requires a token by default (clients send it as `X-Leash-Token`); pass
`--insecure` for a trusted local socket. Without an `X-Loop-Id`, a served request
falls back to a single "default" run.

## How is this different from my framework's max-steps setting?

A max-steps counter lives in memory in one process, counts steps not dollars,
and resets to zero when the process restarts. leash counts what the wire
reported, meters it in dollars against a per-run budget, and writes every call
to a durable journal, so a crash and restart on the same database resume with
the totals intact instead of starting over.

## How is this different from my gateway's budget cap?

A gateway budget cap is usually a per-key total across all traffic on that key.
leash accounts per run: each run has its own budget keyed by `X-Loop-Id` (or the
wrapper default), that budget is durable and survives a restart, and alongside
the token meter there is a compute meter for agents whose real bill is machine
time rather than tokens.

## Can leash tell whether my agent succeeded?

No. leash bounds the economics of a loop - cost, calls, time, rate, repetition,
or an explicit kill. It does not read the work or judge whether the agent
achieved its goal. A run can stop under budget having done nothing useful, or
finish having done everything right; leash only knows the numbers on the wire.

## Where do prices come from?

You supply them with `--prices`, a JSON table mapping each model to dollars per
million input, output, and reasoning tokens:

```json
{"gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0}}
```

leash ships no prices, never hardcodes one, and never estimates tokens. An
unknown model or an absent table means that call's token cost is counted as
zero.

## What happens if the provider does not report usage?

The token meter is blind for that call. When a cost budget is active, leash
fails closed by default (`--on-blind=refuse`): an endpoint it cannot meter is
refused before it is forwarded, and a known-provider call that comes back
unmeterable stops the run, so no spend goes uncounted. `--on-blind=warn` keeps
the older behavior - count zero tokens, warn once, carry on - and `allow`
forwards silently. With no cost budget set, a blind call is harmless and is
allowed; the boundaries that need no token counts (kill, deadline, calls, stall)
still hold the run. leash never guesses a token count to fill the gap.

## Does it break streaming?

No. leash tees the streamed response to your client byte for byte as it arrives
and meters usage on the side; it never buffers a whole stream before passing it
on. Usage is read from the stream's own signals: a final chunk for
OpenAI-compatible responses, `message_start` and `message_delta` for Anthropic.

## What is the include_usage injection, and why?

A streaming OpenAI-compatible response reports usage only in its final chunk, and
only when the request set `stream_options.include_usage`. So for streaming
OpenAI requests leash rewrites the request body to set that option, giving the
token meter something to read. Turn it off with `--no-inject` and accept a blind
token meter on those calls:

```sh
LEASH_AUTH_TOKEN=$(leash gen-token) leash serve --no-inject --max-cost 5.00 --prices prices.json
```

Note that with `--no-inject` and the default `--on-blind=refuse`, a streaming
call that returns no usage will stop the run; set `--on-blind=warn` if you want
those calls to pass while only warning.

## Does leash see or store my API keys or prompt and response bodies?

leash is a proxy, so bytes pass through it, but it does not log or persist them.
Authorization and api-key headers are forwarded to the upstream untouched and
are never logged or written to the ledger. The ledger stores only usage numbers,
content fingerprint hashes, timestamps, and stop reasons - never request or
response bodies.

## What exactly is durable?

Per-run totals. Every governed call, kill, and stop is appended to a durable
ledger (SQLite by default at `$HOME/.leash/leash.db`, or `--db`). Totals are
rebuilt by folding that journal, so:

- a crash and restart on the same `--db` resume with the totals intact;
- a run that was over budget stays stopped after the restart;
- no journal entry is ever counted twice.

A call is counted at-most-once. The only way to lose one is a crash in the
narrow window after the upstream responds but before the journal entry commits,
which undercounts by one and never re-spends.

## Does leash integrate with Slack, PagerDuty, or my ticketing tool?

Not with a built-in connector, on purpose. leash reacts to a stop or a budget
warning through two sinks: a `--webhook` and an `--on-event-exec` command hook
(the event arrives in `LEASH_*` environment variables). Point either at your
tool. With `--reactions-db` set, the reaction is durable: it runs as a retried,
crash-surviving workflow off the enforcement path, delivered at-least-once. A
shipped Jira or Slack connector is exactly where leash would stop being small
enough to audit, so the command hook is the seam instead. See
[`examples/hooks/on-event.sh`](../examples/hooks/on-event.sh).

## Can two leash processes govern the same run at once?

Not safely with the default SQLite backend. Its governance lease is
process-local: it stops two governors inside one process from claiming the same
ledger, but it does not exclude a second process. True cross-process mutual
exclusion requires the postgres backend. Read-only inspection is a different
matter - `leash ps`, `leash inspect`, and `leash kill` read the same database
from any terminal while a run is live.

## What is the exit code when a boundary stops a run?

3, in wrapper (Tier 1) mode. leash otherwise exits with the child's own code, so
a script can tell a governed stop from an ordinary child failure:

```console
$ leash --max-cost 5.00 --prices prices.json -- python my_agent.py
leash: stopped run a3f9 after 18 calls, $4.10 tokens + $0.91 compute = $5.01 (cost_budget)
$ echo $?
3
```

## What does the 429 body look like?

A stopped call is refused with HTTP 429 and a machine-readable JSON body, and
every later call for that run gets the same answer:

```json
{"error":{"type":"leash_boundary","reason":"cost_budget","run":"a3f9",
  "calls":18,"token_cost":4.10,"compute_cost":0.91,"total_cost":5.01}}
```

The agent's own loop ends because its next model call fails, not because leash
signals the process.

## In what order are the boundaries checked?

Always this fixed order, and the first to trip stops the run: kill switch,
deadline, cost budget, max calls, rate limit, stall. The order is a property of
the policy core, not of how you pass the flags. A zero value disables a boundary;
the kill switch is always active and always evaluated first.

## Which providers are supported?

OpenAI-compatible, Anthropic, and Gemini wire formats, both non-streaming and
streaming. Because leash keys on the wire *format*, not the model name, a new
model version needs no code change (add a price-table row), and
"OpenAI-compatible" covers far more than OpenAI: **Gemini and Ollama** through
their OpenAI-compatible endpoints, plus OpenRouter, Groq, Together, vLLM, and the
rest of that ecosystem. leash reads usage from `usage.prompt_tokens` /
`completion_tokens` (plus reasoning, audio, and cached details) for OpenAI,
`usage.input_tokens` / `output_tokens` (plus thinking, cache-TTL split, and
server-tool counts) for Anthropic, and `usageMetadata` for Gemini's native
`generateContent` API. For a local model through Ollama, the honest meter is
`--compute-rate` (machine time), since tokens are effectively free. It infers the
upstream from the request, or you set it with `--upstream`.

## Does leash add latency or buffer my responses?

leash is in the request path: before forwarding it folds the run's journal and
evaluates the boundaries, and after the response it appends one journal entry.
Streamed responses are teed through without being buffered. leash ships no
benchmark numbers and this doc will not invent any - measure it in your own path
if latency matters to you.

## Is it production ready?

leash is v0.x and explicitly unstable: flags and output formats may change
between versions. The durability guarantees are tested, but treat the interface
as not yet frozen, and pin a version.

## What are the non-goals?

Deliberately not built: model routing, a dashboard or web UI, provider SDK
dependencies, anything hosted, request or response body persistence, telemetry
exporters in v1 (an observer seam is left for them), and YAML config - leash is
configured by command-line flags only.

## How do I stop a runaway right now?

Run `leash kill <run>` from any terminal. It records a durable kill for that run,
and the run stops on its next model call with the same 429:

```sh
leash kill a3f9
```

Because the kill is written to the ledger, it holds across a restart and is
evaluated before every other boundary.
