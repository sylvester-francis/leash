# The gateway (Tier 2)

Tier 1 (`leash -- <command>`) wraps one process on one machine and picks the run
id for you. Tier 2 runs leash as a standalone HTTP proxy: a long-lived endpoint
that any client, in any language, can point at. Run it as a per-language
sidecar, a budget guard in CI, or one shared gateway that many agent runs use -
each run carrying its own durable budget.

The engine is the same one the wrapper uses. Every call still folds the run's
journal, evaluates the boundaries in a fixed order, and is refused with an HTTP
429 the moment a boundary trips. What changes in Tier 2 is that leash no longer
launches your process or sets base-url variables for you. You start the server;
the client does two things - aims its base_url at leash, and names its run with
a header.

## Starting the server

```sh
leash serve --listen :8088 --max-cost 5.00 --prices prices.json
```

`serve` accepts the wrapper's meter and boundary flags - `--max-cost`,
`--max-calls`, `--deadline`, `--rate`, `--stall`, `--prices`, `--compute-rate`,
`--upstream`, `--db`, `--no-inject`; see docs/cli-reference.md for the full
reference - plus one flag of its own:

```
--listen   address to listen on   (default :8088)
```

leash binds `--listen`, governs every request that arrives, forwards the allowed
ones upstream, and writes operational logs to stderr. Two of those log lines
matter: one when it comes up, and one each time a run stops. The stop line is
emitted through the observer seam the CLI wires into the proxy - the same seam
left open for future telemetry exporters:

```console
leash: serving on :8088 (db /home/you/.leash/leash.db)
leash: stopped run nightly-batch-7 after 18 calls, $4.10 tokens + $0.91 compute = $5.01 (cost_budget)
```

The server shuts down cleanly on SIGINT or SIGTERM, draining in-flight requests
within a short timeout.

## Pointing a client at it

A client uses leash by setting its SDK base_url to the leash address. leash
speaks the OpenAI and Anthropic wire formats, so nothing else in the client
changes.

OpenAI Python SDK - the base_url ends in `/v1`:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://leash-host:8088/v1",
    api_key="sk-...",  # forwarded upstream untouched
)
resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "hello"}],
)
```

Anthropic Python SDK - the base_url is the host, no `/v1` suffix:

```python
from anthropic import Anthropic

client = Anthropic(
    base_url="http://leash-host:8088",
    api_key="sk-ant-...",  # forwarded upstream untouched
)
msg = client.messages.create(
    model="claude-opus-4-8",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hello"}],
)
```

A raw curl call is the same idea - aim it at the leash host:

```sh
curl http://leash-host:8088/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}'
```

## Run identity is explicit here

In the wrapper, leash chooses a run id and holds it for the child's lifetime. In
the gateway there is no child, so run identity is the client's job: tag each
request with an `X-Loop-Id` header. Every call that carries the same value shares
one durable budget; different values get independent budgets, stops, and kills.

A request with no `X-Loop-Id` falls under the single run id `default`, so all
untagged traffic shares one budget. In serve mode there is no wrapper default to
fall back to - the id is literally `default`. Give each independent agent run
its own `X-Loop-Id` so their budgets stay separate.

leash consumes `X-Loop-Id` as routing and does not forward it upstream; the
provider never sees the header.

Send it once on the client, so every call inherits it:

```python
client = OpenAI(
    base_url="http://leash-host:8088/v1",
    api_key="sk-...",
    default_headers={"X-Loop-Id": "nightly-batch-7"},
)
```

or per call, to set or override it for one request:

```python
resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "hello"}],
    extra_headers={"X-Loop-Id": "nightly-batch-7"},
)
```

The Anthropic SDK takes the same two options - `default_headers` on the client,
`extra_headers` per call:

```python
client = Anthropic(
    base_url="http://leash-host:8088",
    api_key="sk-ant-...",
    default_headers={"X-Loop-Id": "nightly-batch-7"},
)
```

With curl it is just one more header line: `-H "X-Loop-Id: nightly-batch-7"`.

## Provider detection and upstream routing

leash infers the provider from each request so it can meter the right wire
format and forward to the right host. The rule, in order:

- An `Anthropic-Version` header means Anthropic, whatever the path.
- Otherwise the path decides: a path containing `/messages` is Anthropic; a path
  containing `/completions` (which covers `/chat/completions`) or `/responses`
  is OpenAI.
- Anything else is unrecognized.

By default the inferred provider also picks the upstream: OpenAI routes to
`https://api.openai.com`, Anthropic to `https://api.anthropic.com`. An
unrecognized path with no override cannot be routed, and leash answers 502
asking you to set `--upstream`.

`--upstream <url>` overrides inference for every request and sends all forwarded
traffic to that one host. Set it when you want leash in front of:

- a custom or corporate gateway,
- a self-hosted or proxied model endpoint, or
- any endpoint whose path leash does not recognize (so inference alone would
  fail).

```sh
leash serve --listen :8088 --upstream https://gateway.internal --prices prices.json
```

A request leash cannot classify by wire format is still counted as one call and
still subject to the call, deadline, and kill boundaries. Its token count and its
content fingerprint are unread - leash reads those only from formats it
recognizes - so the token-metered boundaries (cost and rate) and stall cannot act
on it; lean on the call, deadline, and kill boundaries for an unfamiliar
endpoint.

## Secrets pass straight through

leash forwards the client's own `Authorization` and `x-api-key` headers upstream
untouched, so the provider authenticates exactly as it would with leash out of
the path. leash never logs and never persists those headers, or any request or
response body. The ledger holds usage numbers, content-fingerprint hashes,
timestamps, and stop reasons - nothing else.

## Use cases

Per-language sidecar. Run one `leash serve` next to a service and point that
service's SDK at it. A Node, Go, Python, or Ruby agent gets durable governance
with no leash library and no code change beyond a base_url.

CI budget guard. Start `leash serve` as a step in a pipeline, point the job's
agent at it, and give the job its own `X-Loop-Id`. Because the budget is durable
on `--db`, a retried or resumed job that reuses the same run id and database
picks up the same account instead of starting fresh.

Shared team gateway. One `leash serve` in front of many agents. Each run carries
its own `X-Loop-Id`, and therefore its own budget, stop, and kill, while all
runs share one price table, one set of boundaries, and one ledger you inspect in
one place.

## Operating a running gateway

The ledger is the source of truth, and it is readable and writable from any
process pointed at the same `--db`. While the server runs, from anywhere on the
same host:

```console
$ leash ps
RUN              CALLS  TOKENS$  COMPUTE$  TOTAL$  STATUS   REASON
nightly-batch-7  18     4.10     0.91      5.01    stopped  cost_budget
api-eval-3       6      1.20     0.00      1.20    running

$ leash inspect nightly-batch-7
run nightly-batch-7  status stopped  calls 18
cost   $4.10 tokens + $0.91 compute = $5.01
tokens in 36000  out 12000  reasoning 0

SEQ  TAG     WHEN                       DETAIL
0    call-0  2026-07-03T15:08:05-04:00  gpt-4o in=2000 out=650 reasoning=0
...
18   stop    2026-07-03T15:12:41-04:00  stop: cost_budget

$ leash kill api-eval-3
leash: kill recorded for run api-eval-3; it stops on its next call
```

`leash kill <run>` records a durable kill; the running server reads it on that
run's next call and returns the 429. You do not restart or signal the server to
stop a runaway - the kill travels through the ledger.

## Running more than one gateway

With the default SQLite backend, the governance lease a server takes on its
ledger is process-local. Cross-process reads and writes - `ps`, `inspect`, and
`kill` against the same `--db` - work today, because SQLite in WAL mode lets a
second process see and append to the ledger while the server holds it. That is
how the operating commands above run against a live gateway.

Running two `leash serve` processes against the same ledger and governing the
same runs is the case that needs more. Coordinating them safely - so two servers
cannot both admit the next call for one run - needs a cross-process lease, which
is the rerun Postgres backend rather than the local SQLite file. For a single
gateway process, the common case, SQLite is enough.
