# ADR-0004: One external dependency, standard-library core

Status: accepted

## Context

leash stands between an agent and its bill, and forwards live provider
credentials. A thing in that position should be small enough to audit in an
afternoon. Every dependency is code you did not write, running with your
credentials, expanding the surface a reviewer must trust and the set of
advisories you must track.

## Decision

Depend on exactly one external module, `rerun`, for durable-execution storage
(the append-only journal and its backends). Everything else is the Go standard
library. The `policy` core has no dependencies at all and no I/O.

Consequences of the rule, applied consistently:
- No web framework, no CLI framework (the stdlib `flag` package), no structured-log
  library (stdlib `slog`), no metrics client (a hand-rolled Prometheus text
  encoder), no YAML.
- No provider SDKs. leash speaks the HTTP wire directly.
- CGO is off; the SQLite backend is pure Go, so releases are static binaries with
  no C toolchain.
- Property-based tests use stdlib `testing/quick` rather than adding a testing
  dependency.

## Consequences

- The trust surface and the vulnerability surface stay small. `govulncheck` scans
  a tiny closure.
- Some things cost more to build (the metrics encoder, usage parsing). We accept
  that; the code is readable and the surface is ours.
- Adding a dependency is a decision that warrants its own ADR, not a default.

## Alternatives considered

- **Reach for the usual libraries.** Faster to write, but each one dilutes the
  "small enough to audit" property that is central to the pitch, and none of them
  is load-bearing enough to justify the cost here.
