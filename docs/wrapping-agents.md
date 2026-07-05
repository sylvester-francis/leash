# Wrapping an agent (Tier 1)

Tier 1 is the headline: put leash in front of any agent and govern every model
call it makes, with no change to the agent's code. leash starts an embedded
proxy, points the provider base-url variables the SDK already reads at that
proxy, runs your command as a child process, and after the child exits prints
one line saying how the run ended.

If your agent talks to an OpenAI-compatible or Anthropic endpoint through the
official SDK - or through plain curl - it already reads the variables leash
sets. That is the whole trick: no SDK to adopt, no `base_url` in your code.

## The model

```sh
leash [flags] -- <command> [args...]
```

Everything before `--` is leash's own flags. Everything after `--` is the child
command and its arguments, run verbatim. The `--` separates the two so leash
never tries to parse your agent's flags as its own.

```sh
leash --max-cost 5.00 --prices prices.json -- python my_agent.py
```

`leash run [flags] -- ...` is the explicit form; a bare `leash -- ...`
dispatches to it. `serve`, `ps`, `inspect`, and `kill` are the other
subcommands.

## What leash does, mechanically

When you run the wrapper, leash:

1. Binds a free port on `127.0.0.1` (it asks the OS for `127.0.0.1:0`), so the
   proxy never leaves your machine.
2. Starts the embedded enforcement proxy on that port in the background.
3. Builds the child's environment from your own (`os.Environ`) plus the three
   provider base-url variables below.
4. Execs the child command, wiring its stdin, stdout, and stderr straight
   through to leash's own - the agent's output is unchanged.
5. Forwards SIGINT and SIGTERM to the child, so Ctrl-C and `kill` reach your
   agent, not just leash.
6. Waits for the child to exit, then reads the run's final totals from the
   durable ledger.
7. Prints one line to stderr - a boundary-stop line or a completion summary -
   and exits.

Every model call the child makes travels to the proxy, is metered and
journaled, and is refused with HTTP 429 the moment a boundary trips. The agent's
own loop ends because its next call fails. leash shuts the proxy down (with a
short grace period) as it exits.

## The environment variables leash injects

leash appends exactly three variables to the child's environment:

```sh
OPENAI_BASE_URL=http://127.0.0.1:PORT/v1
OPENAI_API_BASE=http://127.0.0.1:PORT/v1
ANTHROPIC_BASE_URL=http://127.0.0.1:PORT
```

`PORT` is the free port leash picked. The OpenAI variables include the `/v1`
suffix; the Anthropic variable does not. That difference is deliberate and
matches how each SDK builds a request URL:

- The OpenAI SDK treats its base URL as already ending in `/v1` and appends
  paths like `/chat/completions` to it.
- The Anthropic SDK appends `/v1/messages` itself, so its base URL must not
  already contain `/v1` - otherwise the request would go to `/v1/v1/messages`.

leash appends these to your existing environment rather than replacing it, and
it forwards `Authorization` and `api-key` headers to the upstream untouched.
Your agent still needs its real API key set the usual way; leash only changes
where the request is sent, not who it is authenticated as.

## No code change: examples

Each of these runs unmodified under `leash -- ...` because the SDK (or curl)
reads the base-url variable leash set.

### Python, OpenAI SDK

```python
# my_agent.py - no base_url argument; the SDK reads OPENAI_BASE_URL.
from openai import OpenAI

client = OpenAI()  # OPENAI_API_KEY as usual; OPENAI_BASE_URL points at leash

resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "hello"}],
)
print(resp.choices[0].message.content)
```

```sh
leash --max-cost 5.00 --prices prices.json -- python my_agent.py
```

### Python, Anthropic SDK

```python
# The SDK reads ANTHROPIC_BASE_URL (no /v1) and appends /v1/messages itself.
from anthropic import Anthropic

client = Anthropic()  # ANTHROPIC_API_KEY as usual

msg = client.messages.create(
    model="claude-opus-4-8",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hello"}],
)
print(msg.content[0].text)
```

```sh
leash --max-cost 5.00 --prices prices.json -- python my_agent.py
```

### Node

```js
// The openai package reads OPENAI_BASE_URL from the environment.
import OpenAI from "openai";

const client = new OpenAI();

const resp = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "hello" }],
});
console.log(resp.choices[0].message.content);
```

```sh
leash --max-cost 5.00 --prices prices.json -- node my_agent.js
```

### A plain curl loop

No SDK at all - the shell reads `$OPENAI_BASE_URL`, which already ends in `/v1`:

```sh
leash --max-cost 0.10 --prices prices.json -- \
  sh -c 'for i in $(seq 1 10); do
           curl -s "$OPENAI_BASE_URL/chat/completions" \
             -d "{\"model\":\"gpt-4o\"}" \
             -o /dev/null -w "call $i -> HTTP %{http_code}\n"
         done'
```

Once the budget trips, every remaining call comes back `HTTP 429`, the loop
finishes, and leash prints its stop line.

## Exit codes

leash exits with:

- the child's own exit code on a clean finish (the child ran to completion and
  no boundary stopped the run); or
- `3` when a boundary stopped the run, regardless of the child's own code.

A child killed by a signal reports exit code 1. The code `3` is the
`BoundaryExitCode` constant, and it exists so a script can tell a governed stop
from an ordinary agent failure:

```sh
leash --max-cost 5.00 --prices prices.json -- python my_agent.py
code=$?
if [ "$code" -eq 3 ]; then
  echo "leash stopped the run at a boundary"
elif [ "$code" -eq 0 ]; then
  echo "the agent finished on its own"
else
  echo "the agent exited $code"
fi
```

## Resuming a budget with --run

By default leash gives each invocation a fresh random run id, so every run has
its own budget. Pass `--run NAME` to reuse one durable budget across
invocations: the totals for that name live in the ledger and are reloaded
whenever you run under the same name and `--db`.

```sh
leash --run nightly --max-cost 5.00 --prices prices.json -- python my_agent.py
# later, same name and same --db:
leash --run nightly --max-cost 5.00 --prices prices.json -- python my_agent.py
```

Because the budget is durable, this survives a process restart or a crash. If
the named run was already over budget, the run's next call is refused
immediately - the agent stops on its first request and leash exits 3. The name
and the `--db` path must match for the budget to be found; the default `--db` is
`$HOME/.leash/leash.db`, so leaving it unset is enough.

## The lines leash prints to stderr

leash prints exactly one summary line to stderr when the child exits.

When a boundary stopped the run:

```console
leash: stopped run <id> after N calls, $X tokens + $Y compute = $Z (reason)
```

for example:

```console
leash: stopped run a3f9 after 18 calls, $4.10 tokens + $0.91 compute = $5.01 (cost_budget)
```

`reason` names the boundary that tripped (for example `cost_budget`); the full
set is in [`docs/boundaries.md`](boundaries.md).

When the child exited on its own with no boundary stop:

```console
leash: run <id> finished after N calls, $X tokens + $Y compute = $Z (child_exited)
```

The `(child_exited)` tag distinguishes a clean finish from a stop. Both lines go
to stderr, so they never mix into the agent's own stdout.

## Troubleshooting

**The agent ignores the base-url variables.** A few SDKs and HTTP clients do not
read `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` from the environment and take the
base URL only as a constructor argument. In that case, read the value leash set
and pass it explicitly, for example
`OpenAI(base_url=os.environ["OPENAI_BASE_URL"])`. Some clients also use a
different variable name; check the SDK's own docs for which one it honors.

**The cost budget never trips.** leash meters only the token usage the provider
reports on the wire, so a call with no usage cannot be priced. Under a cost budget
with the default `--on-blind=refuse`, a blind call is delivered once and then
stops the run (`meter_blind`) - so "never trips" usually means you set
`--on-blind=warn`/`allow`, or have no cost budget. Common causes of a blind meter:
no `--prices` table (or a model absent from it), which prints a one-time warning;
or an upstream that reports no usage. If you deliberately run `warn`/`allow`, lean
on the boundaries that need no token counts: `--max-calls`, `--deadline`, and
`--stall` (the `--rate` limit meters tokens, so it is blind too). A bare
`leash -- <command>` still enforces the default call and time limits even with no
prices.

**Your endpoint is not a standard chat/completions or messages path.** By
default leash infers the upstream to forward to from the request. If your
provider lives at a nonstandard host or path, set `--upstream <base URL>` to the
base URL leash should forward every call to.
