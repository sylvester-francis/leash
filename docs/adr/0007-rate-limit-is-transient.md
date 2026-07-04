# ADR-0007: The rate limit is backpressure, not a terminal stop

Status: accepted (changed in v0.2)

## Context

Every leash boundary was terminal: once a run tripped one, it stayed stopped for
good (ADR: stopped-stays-stopped). That is correct for a cost budget, a call cap,
a deadline, a stall, and a kill, which are all "this run is done." But a *rate*
limit is different in kind. A trailing-window token rate is a statement about
speed, not a total, and the window decays. Treating "you went too fast for a
moment" as "this run is dead forever" is surprising and wrong: the natural
recovery is to slow down and continue, not to abandon the run.

## Decision

The rate limit is the one **transient** boundary. When it trips, leash refuses the
call with a 429 that carries a `Retry-After` header (the window in seconds) and
**does not stop the run**. Once the trailing window decays below the limit, the
run's calls proceed again. A well-behaved client honors `Retry-After` and retries.
Every other boundary remains terminal.

## Consequences

- A rate-limited run shows as `running`, not `stopped`, in `ps`, which is correct:
  it is throttled, not dead.
- A rate-limit refusal is not recorded as a durable stop; it is a transient control
  response, so there is no journal entry to reconcile.
- The boundary reason enumeration gains one member whose semantics differ from the
  rest, which the docs call out explicitly.

## Alternatives considered

- **Keep it terminal.** Simpler and uniform, but it makes the rate limit unusable
  for its actual purpose (smoothing a burst) because any burst kills the run.
- **A full queue with delayed release.** Real backpressure, but it holds requests
  and complicates the proxy's memory and lifecycle. `Retry-After` pushes the wait
  to the client, which is where an agent's own loop already lives.
