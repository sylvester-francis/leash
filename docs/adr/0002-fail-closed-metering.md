# ADR-0002: Fail closed when a call cannot be priced

Status: accepted (changed the default in v0.2.0)

## Context

leash meters cost by reading a usage block off the provider's response. Sometimes
it cannot: an unrecognized endpoint, a response shape it does not understand, or a
body mis-tagged for a different provider. The original behavior was to count that
call as zero and forward it anyway. That is a fail-*open* meter, and it is the most
dangerous failure a spend governor can have, because it does not error; it lies. A
real, billed call slips past the budget recorded as free, and the operator has no
signal until the invoice.

## Decision

When a cost budget is active and leash cannot price a call, it **fails closed** by
default (`--on-blind=refuse`):

- An `Unknown`-provider call is refused with 402 before it is forwarded.
- A known-provider call that comes back unreadable is delivered once (the upstream
  already billed it) and then the run is stopped, so no further spend goes
  uncounted.

`--on-blind=warn` restores the old count-zero-and-continue behavior for operators
who want it; `--on-blind=allow` is silent. With no cost budget set, a blind call is
harmless to the budget and is allowed.

## Consequences

- Measurement is conservative: the meter refuses rather than under-reports.
- This is a breaking default change, shipped in v0.2.0 and documented as such.
- `HasUsage` is true only when the provider's expected fields are actually present,
  so a present-but-wrong usage object (the mis-tag case) reads as blind, not zero.

## Alternatives considered

- **Estimate the tokens.** leash never invents a number it was not given; an
  estimate that is wrong in the unsafe direction reintroduces the same lie.
- **Keep fail-open, warn louder.** A warning in a log is not a control. Under a
  cost budget, the only safe default is to stop spending you cannot measure.
