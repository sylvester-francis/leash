# ADR-0001: The journal is the source of truth

Status: accepted

## Context

leash must not let a crash reset a run's budget. An in-memory counter, or a
mutable "total" row updated in place, loses its meaning the moment the process
dies mid-update: you cannot tell whether the last increment landed, and a restart
starts from whatever partial value survived. For a tool whose entire job is to
enforce a spending limit, "the number is approximately right unless we crashed" is
not acceptable.

## Decision

Every governed event (a call, a kill, a stop) is written as an immutable entry in
an append-only journal, keyed by run and sequence. A run's totals are never stored
directly; they are **rebuilt by folding the journal** through the deterministic
`policy.State.Fold`. The journal is the single source of truth. In-memory state is
only ever a cache of a fold.

## Consequences

- A restart re-folds the journal and arrives at the exact prior total. A run that
  was over budget stays over budget.
- Recovery is replay, not repair: there is no partial-write reconciliation because
  there are no in-place updates to reconcile.
- Reads are cheap on the warm path (fold incrementally) and O(journal) on a cold
  load. This bounds throughput per governor, which we accept ([ADR-0004],
  [ADR-0008]).
- The fold must be deterministic, which constrains `policy` to be pure.

## Alternatives considered

- **A mutable total with write-ahead logging.** More machinery for the same
  guarantee, and it invites in-place-update bugs the append-only model cannot have.
- **Trust the provider's own usage dashboards.** Reported after the money is spent,
  aggregated across keys, and not per-run. It answers a different question.

The durable-execution mechanics (journal storage, crash-safe replay) are provided
by the `rerun` library; leash owns the folding and the enforcement on top.

[ADR-0004]: 0004-one-dependency.md
[ADR-0008]: 0008-warm-cache-equals-cold-fold.md
