# ADR-0005: The proxy depends on a Ledger interface

Status: accepted

## Context

The proxy's most important behavior is what it does when the durable write
*fails*: it must fail closed rather than keep forwarding unmetered spend. That
behavior is impossible to test against a real database, because you cannot make a
real SQLite write fail on demand while its reads keep working (the disk-full,
read-only-remount failure mode). If the proxy holds a concrete `*ledger.Ledger`,
the fail-closed path is untestable, and an untested fail-closed path is a
fail-open path you have not noticed yet.

## Decision

The proxy depends on a `Ledger` **interface** that captures exactly the methods it
uses (`Acquire`, `EnsureRun`, `LoadAt`, `CancelRequested`, `AppendCallAt`,
`AppendStop`, `Ping`). The real `*ledger.Ledger` satisfies it in production; tests
substitute a fake whose writes fail while its reads succeed.

## Consequences

- The ledger-writes-fail-closed path is covered by a test that drives call 1 (200,
  already billed), call 2 (503, refused), and recovery (200). The invariant is
  proven, not asserted in a comment.
- The interface is the proxy's, defined at the point of use and kept minimal, so it
  does not leak the ledger's full surface.
- A future alternative ledger (e.g. a sharded one for horizontal scale) has a
  narrow contract to satisfy.

## Alternatives considered

- **Test against a real database with fault injection at the driver.** Heavier, and
  it couples the test to a specific backend's failure semantics rather than the
  contract the proxy actually depends on.
- **Skip the test.** The review found this path failing open precisely because it
  was untested. Not an option.
