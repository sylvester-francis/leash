# The cost model

leash has one budget and two meters that feed it. The token meter prices the
tokens a call actually reported; the compute meter prices the wall-clock time the
run has been alive. Their sum is the number `--max-cost` is checked against. Every
figure comes from a price table you supplied or from what the provider wire
reported; leash ships no prices and never estimates a token.

## Two meters, one budget

A run's total cost is the sum of the two meters:

```
total_cost = token_cost + compute_cost
```

`--max-cost` is the cost budget. It trips when `total_cost` reaches the budget:
the check is `total_cost >= budget`, so an exact hit stops the run. The two meters
are independent - either one can move the total on its own, and either alone can
trip the budget.

| Meter   | Priced from                          | Flag             | Default |
|---------|--------------------------------------|------------------|---------|
| token   | a price table you supply             | `--prices`       | none    |
| compute | elapsed wall-clock time times a rate | `--compute-rate` | 0       |

The 429 body and the stop line both report the split, so you can always see which
meter spent the money:

```json
{"error":{"type":"leash_boundary","reason":"cost_budget","run":"a3f9",
  "calls":18,"token_cost":4.10,"compute_cost":0.91,"total_cost":5.01}}
```

Here the two meters summed to $5.01, which reached a $5.00 budget. The cost
budget is one of six boundaries evaluated in a fixed order; see
[`boundaries.md`](boundaries.md) for that ordering.

## The token meter

### The price table format

A price table is a JSON object mapping a model name to its price in dollars per
million tokens. The core rates are `input`, `output`, `reasoning`, and the
optional cache rates `cached_input` (cache reads) and `cache_write` (cache
creation). An omitted cache rate falls back to the `input` rate.

```json
{
  "gpt-4o": {"input": 2.5, "output": 10, "reasoning": 0, "cached_input": 1.25},
  "gpt-4":  {"input": 30,  "output": 60, "reasoning": 0},
  "o1":     {"input": 15,  "output": 60, "reasoning": 60}
}
```

Point leash at the file with `--prices`:

```sh
leash --max-cost 5.00 --prices prices.json -- python my_agent.py
```

Two loader details worth knowing:

- Unknown fields inside a model's price object are rejected: a key leash does not
  recognize makes the whole table fail to load, so a typo is a startup error, not
  a silent zero.
- A rate you omit defaults to zero, and each refinement below falls back to a
  coarser rate, so `{"m": {"input": 2.5}}` still prices output and reasoning at
  zero and needs no other keys.

#### Refining the price (opt-in)

When you need them, optional fields price finer dimensions; leave them out and
nothing changes. All are per million tokens except the per-request tool rates:

- `audio_input` / `audio_output`: audio tokens (a subset of input / output); fall
  back to `input` / `output`.
- `cache_write_5m` / `cache_write_1h`: the 5-minute and 1-hour TTL portions of
  cache creation; fall back to `cache_write`.
- `web_search_per_request` / `web_fetch_per_request`: dollars per provider-side
  tool request (not tokens). Unset means the tool is unpriced, so a call that uses
  it fails closed under a cost budget rather than billing at zero.
- `tiers`: a map of service tier to a full price override. When a call reports a
  matching `service_tier` (for example `priority` or `batch`), that tier's price
  replaces the base for the call.

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

### The cost of one call

For a call whose model is in the table, the cost is linear in each token count.
Reasoning tokens are a **subset** of the reported output tokens (OpenAI's
`completion_tokens` includes them), so they are priced once, never twice:

```
reasoning_rate := table[model].reasoning, or output_rate when that is 0
token_cost = input               / 1,000,000 * input_rate
           + (output - reasoning) / 1,000,000 * output_rate
           + reasoning            / 1,000,000 * reasoning_rate
```

Worked against the table above:

| Model | input     | output    | reasoning | token_cost              |
|-------|-----------|-----------|-----------|-------------------------|
| gpt-4 | 1,000,000 | 1,000,000 |         0 | 30 + 60 = 90.00         |
| gpt-4 |   500,000 |   250,000 |         0 | 15 + 15 = 30.00         |
| o1    | 1,000,000 | 1,000,000 |   500,000 | 15 + 30 + 30 = 75.00    |

A run's token cost is the sum of every call's cost, accumulated as each call is
journaled.

### Reasoning tokens

Reasoning (or "thinking") tokens are part of the output the provider reports (for
example OpenAI's `completion_tokens` already includes
`completion_tokens_details.reasoning_tokens`). leash therefore prices the
non-reasoning output at the `output` rate and the reasoning subset at the
`reasoning` rate - each token once. When a model has no reasoning rate
(`"reasoning": 0`, like `gpt-4o`), reasoning tokens fall under the `output` rate,
so they are still priced, just not separately.

### Cached tokens

Providers report prompt-cache usage, and it is billed differently from fresh
input: cache reads are cheaper, cache writes a little dearer. leash reads both
wire shapes - OpenAI's `prompt_tokens_details.cached_tokens` (a subset of
`prompt_tokens`) and Anthropic's separate `cache_read_input_tokens` /
`cache_creation_input_tokens` - and prices the cached portion at `cached_input`
and the written portion at `cache_write`. Because an omitted cache rate falls
back to `input`, leaving them out is safe (it just prices cached tokens at the
full input rate, as before), and setting them makes the meter match a provider
invoice for a cache-heavy agent instead of over-counting it.

### leash never guesses

Two rules that never bend:

- **leash never hardcodes a price.** Every rate comes from the table you passed.
  There is no built-in table and no per-provider default.
- **leash never estimates tokens.** Counts come only from what the provider wire
  reported (a `usage` block in JSON, or the final chunk / `message_delta` of a
  stream). See [`metering.md`](metering.md) for the wire formats.

The consequence is that any call leash cannot price costs exactly zero on the
token meter:

- an **unknown model** - a model name absent from the table - costs zero;
- an **absent table** - no `--prices` at all - costs zero for every call;
- a call whose **wire reported no usage** costs zero, because there are no counts
  to price.

Zero here means "not measured," not "free" - and unmeasured spend is exactly what
a governor must not wave through. So under a cost budget, a blind call does not
just cost zero and continue: by default (`--on-blind=refuse`) leash fails closed,
delivering the already-billed call once and then stopping the run (`meter_blind`).
The same holds for billed activity it cannot price from the table, such as an
unpriced provider-side tool request (`server_tool_unpriced`). With
`--on-blind=warn` or `allow`, or with no cost budget, the call costs zero and the
run continues, leaning on the boundaries that need no token counts (calls,
deadline, stall, kill). The rate limit is not among those: it meters tokens per
window, so a blind call is invisible to it too.

## The compute meter

The compute meter prices machine time, for a self-hosted agent whose real bill is
the box it runs on rather than per-token API charges. It is elapsed wall-clock
time since the run's first call, at `--compute-rate` dollars per hour:

```
compute_cost = elapsed_hours * compute_rate
```

The rate defaults to zero, which makes compute free - the honest default when
leash cannot see the machine bill. A zero rate or zero elapsed time yields zero.

Worked examples:

| elapsed | rate ($/hour) | compute_cost |
|---------|---------------|--------------|
| 1 hour  | 1.00          | 1.00         |
| 30 min  | 2.00          | 1.00         |
| 15 min  | 4.00          | 1.00         |
| 1 hour  | 0             | 0.00         |

Because compute is priced from the clock, not the wire, it accrues even on calls
the token meter is blind to - which is what lets the budget still trip when you
have a compute rate but no prices.

## When the meter is blind

If you set `--max-cost` but give leash nothing that can move the total - no
`--prices` and a `--compute-rate` of 0 - then no call can ever change the cost and
the budget can never trip. leash detects exactly this at startup and warns, once
and loudly, on stderr:

```console
leash: token meter blind: supply --prices, or the cost budget cannot trip
```

The warning fires precisely when all three hold: `--max-cost` is above zero,
there is no price table, and the compute rate is zero. The cost budget defaults to
`5.00`, so a run that sets no budget flags at all still lands in this case - a
default budget with no live meter is exactly what the warning is about. Loosen any
one of the three and the budget becomes enforceable:

- **Supply `--prices`.** The token meter goes live and prices every known model.
- **Set `--compute-rate` above zero, even with no prices.** The compute meter
  alone can now reach the budget as the run's wall-clock time grows across its
  calls, so leash does not warn. The token meter stays at zero (no prices), but
  `total_cost` still climbs on the compute side.

This startup warning is distinct from a single call going blind at runtime. Even
with a good price table, a call whose model is unknown or whose wire carried no
usage cannot be priced. Under the default `--on-blind=refuse` that call is
delivered once and then stops the run (`meter_blind`); with `--on-blind=warn`
leash instead logs a per-run notice
(`token meter blind (no usage on the wire); relying on other boundaries`) the
first time and keeps going.

## Worked example: the 60-second demo

The demo in the README exercises the token meter end to end. The price table gives
one model input at $10 per million and output at $30 per million:

```json
{"demo-model": {"input": 10.0, "output": 30.0, "reasoning": 0}}
```

The fake upstream reports the same usage on every call: 1000 input tokens and 500
output tokens. So each call costs:

```
1000 / 1,000,000 * 10   +   500 / 1,000,000 * 30
= 0.01                  +   0.015
= 0.025
```

Two and a half cents per call. Under a ten-cent budget:

```console
agent call 1 -> HTTP 200
agent call 2 -> HTTP 200
agent call 3 -> HTTP 200
agent call 4 -> HTTP 200
agent call 5 -> HTTP 429
```

The first four calls are forwarded and journaled, accumulating 4 * 0.025 = $0.10.
The fifth call is evaluated before it is forwarded: the running total is already
$0.10, which reaches the $0.10 budget (`0.10 >= 0.10`), so the boundary trips and
the call is refused. Nothing is metered on the fifth call, which is why the stop
line counts four:

```console
leash: stopped run demo after 4 calls, $0.10 tokens + $0.00 compute = $0.10 (cost_budget)
```

Compute is $0.00 here because the demo sets no compute rate; the entire budget was
spent on the token meter.

## What the meter does not model

The meter prices tokens and per-request tool charges from the rates you supply
(input/output/reasoning, cache-read/write and its TTL split, audio, per-request
web-search/fetch, and per-service-tier overrides; see "Refining the price"
above). Context-length-dependent rates and batch-window discounts beyond a
service tier are not modeled, and are not reflected in leash's total. Prices are
entirely caller-supplied: leash ships no price table, applies no provider
defaults, and updates no rates on your behalf. Keeping the table current, and
choosing rates that match your contract, is your responsibility.
