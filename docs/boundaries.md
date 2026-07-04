# The boundaries

A boundary is one stopping condition. leash evaluates the active boundaries in a
fixed order before every call and refuses the call the moment the first one
trips. This guide covers each boundary, when to reach for it, and its edges. The
implementations are small, pure, and table-tested in
`internal/policy/boundary.go`.

## How evaluation works

Before a call is forwarded, `Governor.Evaluate` refreshes the run's elapsed time
and compute cost, then checks each active boundary in this order and returns the
first that trips:

```
1. kill switch   always active
2. deadline      --deadline
3. cost budget   --max-cost
4. max calls     --max-calls
5. rate limit    --rate tokens/window
6. stall         --stall
```

Two rules follow from this design:

- **A zero-valued limit disables that boundary.** It is simply left out of the
  list. The kill switch is the exception: it is always present.
- **Boundaries judge the state accumulated so far, before the current call.** So
  a limit of N lets N calls through and refuses the N+1th. leash cannot know a
  call's cost until it has been made, so it bounds the loop at the next call, not
  mid-call.

The order is a property of the core, set once in `NewGovernor`, so it cannot be
reordered by accident from the outside.

## Kill switch

Always active. Trips when a durable kill has been recorded for the run.

```sh
leash kill <run>            # from any process against the same --db
```

`leash kill` appends a `kill` entry to the journal. The governor sees it when it
folds the journal on the run's next call, and stops. This is how you halt a
runaway right now, from a second terminal, even though a different process is
doing the governing. See [durability.md](durability.md).

## Deadline

```sh
--deadline 15m             # default 30m; 0 disables
```

Trips when wall-clock time since the run's first governed call reaches the limit.
The clock starts at the first call, not at process start, and it is durable: the
first call's timestamp is in the journal, so a restart measures the deadline from
the original start, not from the restart.

Reach for it to bound how long an unattended run may take regardless of cost - a
cheap loop that never terminates is still a problem.

## Cost budget

```sh
--max-cost 5.00            # default 5.00; 0 disables
```

Trips when the run's total cost - token cost plus compute cost - reaches the
budget. This is the headline boundary: "this run cannot spend more than $X."
Because a call's cost is known only after the upstream serves it, the call that
crosses the budget still completes: the budget can overshoot by one in-flight
call. Bound that call with `--max-cost-per-call`, which stops the run if any
single call's token cost exceeds the cap.

It only trips if a meter is live. If you set `--max-cost` but supply neither
`--prices` nor a `--compute-rate`, the total cost stays zero; leash warns you,
once and loudly, that the token meter is blind. And because it cannot price
calls, the default `--on-blind=refuse` makes it fail closed - refusing an
unmeterable endpoint and stopping a run whose call comes back unmeterable -
rather than spend blindly (set `--on-blind=warn` to only warn). Supply a price
table (`--prices`) to meter tokens, a compute rate (`--compute-rate`) to meter
machine time, or both. See [cost-model.md](cost-model.md).

## Max calls

```sh
--max-calls 200            # default 100; 0 disables
```

Trips when the run has made that many governed calls. Every call leash forwards
is counted, including calls the provider answered with an error, so a loop stuck
retrying a failing request is still bounded. This is the cheapest, most
provider-independent guard: it needs no prices and no usage on the wire.

## Rate limit

```sh
--rate 100000/1m           # tokens/window; off by default
```

Trips when the tokens consumed within a trailing window exceed the maximum. The
value is `tokens/window`, for example `100000/1m` for a hundred thousand tokens
per minute; the window is any Go duration (`30s`, `1m`, `1h`). Both parts are
required - a token count with no window, or a window with no token count, leaves
the rate limit disabled.

The window is computed from the recorded per-call token samples, so it decays: a
run that goes quiet for longer than the window sees its rate fall back toward
zero. The comparison is strict - a window delta exactly at the maximum does not
trip. Reach for the rate limit to catch a loop that accelerates, spending faster
and faster, before its total budget is reached.

## Stall

```sh
--stall 3                  # identical responses in a row; off by default
```

Trips when that many consecutive responses carry the same content fingerprint -
leash's signal that the agent is redoing identical work. The fingerprint is a
SHA-256 of the normalized assistant text (whitespace collapsed), so cosmetically
different but identical answers match, and a genuinely new answer resets the
streak.

A blind call - one where leash could read no assistant text - cannot be a repeat,
so it resets the streak rather than extending it. Reach for stall to catch the
"finishes, doubts itself, redoes the same work" failure mode, where cost and call
count climb but nothing new is produced.

## Choosing a set

The boundaries are complementary; a real deployment usually sets several:

- `--max-cost` with `--prices` is the dollar ceiling you actually care about.
- `--max-calls` and `--deadline` are cheap backstops that work even when the
  token meter is blind, and they are on by default for that reason.
- `--rate` catches acceleration before the total budget is hit.
- `--stall` catches repetition that a cost cap would only catch much later.

Everything a boundary reports - the reason string, the calls, and the three cost
figures - shows up in the 429 body and the stop line, so whichever one fired is
always named. See [cli-reference.md](cli-reference.md) for the exact flag syntax
and [how-leash-works.md](how-leash-works.md) for the evaluation model.
