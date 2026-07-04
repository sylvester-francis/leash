# Architecture decision records

These record the decisions that shaped leash, and the alternatives rejected, so a
future reader (including a future maintainer) can see not just what leash does but
why it does it that way. Each is short and dated at the point the decision was
made. A decision that is later reversed is superseded, not deleted.

Format: context, the decision, its consequences, and the alternatives weighed.

| # | Decision |
|---|---|
| [0001](0001-append-only-journal.md) | The journal is the source of truth; state is a fold of it |
| [0002](0002-fail-closed-metering.md) | Fail closed when a call cannot be priced |
| [0003](0003-at-most-once-counting.md) | Count at most once; undercount by one is acceptable, over-count is not |
| [0004](0004-one-dependency.md) | One external dependency, standard-library core |
| [0005](0005-ledger-interface.md) | The proxy depends on a Ledger interface, not the concrete type |
| [0006](0006-tenancy-by-credential.md) | Scope runs by the presenting credential when auth is on |
| [0007](0007-rate-limit-is-transient.md) | The rate limit is backpressure, not a terminal stop |
| [0008](0008-warm-cache-equals-cold-fold.md) | The warm cache must equal a cold fold of the journal |
