# ADR-0003: Count at most once

Status: accepted

## Context

A call can be recorded, or not, around a failure. There are two ways to be wrong:
count a call that did not happen (over-count) or miss a call that did
(under-count). A spend governor must pick which error it tolerates, because at a
failure boundary it cannot always have neither.

Over-counting stops a run early: the agent is refused before it truly hit the
budget. Annoying, but it never spends money it should not have. Under-counting
lets one extra call through. Also bounded, and it errs toward completing work. The
unacceptable outcome is a *systematic* over-count that repeatedly refuses a run
with budget left, or an *unbounded* under-count that never stops.

## Decision

Guarantee **at most once**: a call is counted at most once, and any error at a
boundary undercounts by at most one, never over. Concretely:

- The call record is appended **after** the response is delivered. A crash between
  delivering and recording loses that one call from the total (safe).
- The record is written with a detached context, so a client disconnect cannot
  drop it (that would be an under-count the client controls, i.e. an evasion).
- Appends are **idempotent by tag**: a durable write that commits but returns an
  error to its caller is not re-recorded on retry, so a call cannot land twice
  (ADR-0001's fold would otherwise count it twice). See issue #26.

## Consequences

- The worst case is a single uncounted call per unclean failure, per run. Bounded
  and safe.
- Tests assert this as a property: N distinct calls with retries and faults
  injected fold to exactly N.

## Alternatives considered

- **Record before forwarding (at-least-once).** Then a failure over-counts: a run
  is charged for a call that may never have reached the upstream, and can be
  refused with budget left. Wrong direction for a governor.
- **Two-phase commit across the proxy and ledger.** Far more machinery to turn a
  bounded, safe under-count into an exactly-once that users cannot perceive.
