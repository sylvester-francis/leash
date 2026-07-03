# Getting started

This walks you from an empty shell to a governed agent run, then to the gateway
and the ledger tools. It assumes Go 1.25+ and nothing else.

## Install

```sh
go install github.com/sylvester-francis/leash/cmd/leash@latest
```

Or build from a checkout:

```sh
git clone https://github.com/sylvester-francis/leash
cd leash
make build        # produces ./leash
```

leash builds as a single static binary with no C toolchain: the SQLite ledger is
pure Go via modernc.org/sqlite.

## A first run with no API key

The repo ships a standard-library fake provider so you can see leash stop a loop
without spending anything. In one terminal, from a checkout:

```sh
go run ./examples/fakeupstream
# fakeupstream listening on http://127.0.0.1:9099
```

In another terminal, give the token meter prices (leash ships none) and run a
curl loop under a 10-cent budget:

```sh
echo '{"demo-model": {"input": 10.0, "output": 30.0, "reasoning": 0}}' > prices.json

leash --max-cost 0.10 --prices prices.json \
  --upstream http://127.0.0.1:9099 --db ./demo.db --run demo -- \
  sh -c 'for i in $(seq 1 10); do
           curl -s "$OPENAI_BASE_URL/chat/completions" -d "{\"model\":\"demo-model\"}" \
             -o /dev/null -w "agent call $i -> HTTP %{http_code}\n"
         done'
```

You will see four calls succeed, the rest refused, and one stop line:

```console
agent call 1 -> HTTP 200
agent call 2 -> HTTP 200
agent call 3 -> HTTP 200
agent call 4 -> HTTP 200
agent call 5 -> HTTP 429
...
leash: stopped run demo after 4 calls, $0.10 tokens + $0.00 compute = $0.10 (cost_budget)
```

leash exits with code 3 to mark the boundary stop. Check it:

```sh
echo $?   # 3
```

## What just happened

leash picked a free local port, started an embedded proxy, and set
`OPENAI_BASE_URL` (and its legacy alias `OPENAI_API_BASE`) in the child's
environment to point at that port. The curl loop read `$OPENAI_BASE_URL` and
sent its calls through leash, which metered each one against the price table,
journaled it to `./demo.db`, and refused the call that would have crossed the
budget. See [wrapping-agents.md](wrapping-agents.md) for the mechanics.

## Wrapping a real agent

Because the SDK already reads the base-url variables, a real agent needs no code
change. Supply a price table for your models and pick the boundaries you want:

```sh
leash \
  --max-cost 5.00 \
  --max-calls 200 \
  --deadline 15m \
  --stall 3 \
  --prices prices.json \
  -- python my_agent.py
```

Without `--upstream`, leash infers the provider from each request path and
forwards to api.openai.com or api.anthropic.com. Your API key is read by your
agent as usual and forwarded upstream untouched; leash never logs or stores it.

## Running the gateway

For any language, CI, or a shared team gateway, run leash as a standalone proxy
and point your agent's `base_url` at it:

```sh
leash serve --listen :8088 --max-cost 5.00 --prices prices.json
```

The client tags each run with an `X-Loop-Id` header so each gets its own durable
budget:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8088/v1",
    default_headers={"X-Loop-Id": "nightly-batch-42"},
)
```

See [gateway.md](gateway.md) for the full guide.

## Inspecting and stopping runs

From any terminal against the same `--db`:

```console
$ leash ps --db ./demo.db --prices prices.json
RUN   CALLS  TOKENS$  COMPUTE$  TOTAL$  STATUS   REASON
demo  4      0.10     0.00      0.10    stopped  cost_budget

$ leash inspect demo --db ./demo.db --prices prices.json
run demo  status stopped  calls 4
cost   $0.10 tokens + $0.00 compute = $0.10
tokens in 4000  out 2000  reasoning 0
...

$ leash kill demo --db ./demo.db
leash: kill recorded for run demo; it stops on its next call
```

`leash kill` works from a second process while a run is live: the governor sees
the durable kill on the run's next call and stops it. See
[durability.md](durability.md).

## Resuming a budget

Reuse a run name to resume its durable budget on a later invocation. This run
picks up wherever the last one under `nightly` left off, against the same `--db`:

```sh
leash --run nightly --max-cost 20.00 --prices prices.json --db ./team.db -- python my_agent.py
```

## Where to go next

- [how-leash-works.md](how-leash-works.md) - the enforcement model and durability, concept to code.
- [boundaries.md](boundaries.md) - every boundary and when to reach for it.
- [cost-model.md](cost-model.md) - price tables and the two meters.
- [cli-reference.md](cli-reference.md) - every command, flag, and exit code.
