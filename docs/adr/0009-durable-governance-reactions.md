# ADR-0009: Governance reactions are durable, off the hot path, and integration-free

Status: accepted (implemented in `internal/reactions`)

## Context

When a run trips a boundary or approaches one, leash reacts through a best-effort
`Observer`: a log line and, optionally, a webhook. That path has no context, no
persistence, and no recovery, so a crash mid-reaction or a webhook that is down
simply loses the escalation. For a governor whose job is to stop expensive runs,
"we stopped the $10k run but the alert to your ops channel was lost" is a real
weakness.

rerun, already the single dependency, is a durable-execution engine, and leash so
far uses only its `Store` layer. Reactions are workflow-shaped
(`notify` then `run a hook`, each retried), so this is the natural place to use
rerun's execution layer and become a genuine two-layer consumer. The mechanics
below are verified against rerun v0.2.0 source; **zero rerun changes are
required**, which is itself a validation of the engine's API.

## Decision

Reactions run as a rerun workflow, with these boundaries:

1. **A second, physically separate Engine and store** (`--reactions-db`: its own
   SQLite file or Postgres schema). Separation is mandatory, but not for lease
   reasons: rerun's `Acquire` is per-run (SQLite is an in-process per-run map;
   Postgres is `pg_try_advisory_lock(hash(runID))`), so there is no cross-talk on
   the lease. The real reasons are that `Incomplete()` has no workflow filter, so a
   shared store would let `leash ps` list reaction runs and let the reactions
   `Recover()` scoop up the long-lived `Running` governance runs, and that SQLite
   is single-writer, so reactions would serialize behind governance writes. A
   separate store makes the isolation structural.

2. **Two sinks only, no integrations.** The existing webhook and a generic
   `--on-event-exec` command hook (run metadata passed as `LEASH_*` environment
   variables, no shell interpolation). Jira, PagerDuty, and the like are things a
   user wires through the command hook, never connectors leash carries. The day
   leash ships a connector it stops being auditable in an afternoon ([ADR-0004]).

3. **At-least-once, deliberately the opposite of the ledger.** The ledger
   undercounts to stay safe ([ADR-0003]); a reaction inverts that, because a
   duplicate alert beats a missed one. Idempotency is free: the reaction run id is
   the event id (`stop:<runID>`, `warn:<runID>:<budget>`), so a duplicate enqueue
   hits the `runs` primary key, `Create` returns `ErrRunExists`, and the worker
   treats that as already-enqueued.

4. **Opt-in and non-breaking.** With no `--reactions-db`, reactions stay
   best-effort exactly as today.

5. **A named, minimal surface.** Nine rerun symbols, zero new modules: `rerun.New`,
   `(*Engine).Handle` / `.Start` / `.Recover` / `.Shutdown`, `rerun.Do` / `Retry`,
   `RetryPolicy` with `ExpBackoff` / `FixedBackoff`, `rerun.Input`, and the types
   `W` / `Func`. `Result`, `Sleep`, `Cancel`, and signals stay out.

## The enqueue seam (the one real tradeoff)

`rerun.Start` is a synchronous `store.Create`, not a cheap in-memory enqueue. That
collides with the hard constraint that the enforcement path never waits on a
reaction. The constraint wins: the `ReactionDispatcher` observer does a
non-blocking channel send and returns, and a background worker calls `Start`.

The honest cost, stated rather than hidden: durability begins at the worker's
`Create`, not at the instant of the stop. A crash in the window
`[stop -> Create commits]` drops that one reaction, and it will not re-fire,
because `RunStopped` is a live-only transition: it fires only on the call that
first trips a boundary, while a restarted stopped run cold-loads and takes the
already-stopped branch (`StopReason != ""`), which fires `CallRefused`, never
`RunStopped`. So "durable and crash-surviving" is true from the enqueue onward,
with a bounded gap at the seam.
A future boot-reconciliation sweep (scan the governance ledger for `stop` / `warn`
records with no matching reaction run and enqueue them, idempotent via the
event-id key) closes the window; it is deferred until the window is shown to
matter, because it reintroduces a cross-store read.

## Consequences

- Escalations become durable, retried, and crash-surviving off the hot path, while
  the enforcement path and its fail-closed / at-most-once guarantees are untouched.
- leash becomes a true two-layer consumer of rerun: execution, not just storage.
- One bounded, documented gap remains at the enqueue seam, closable later.
- Boot `Recover()` for reactions is gated on winning the governance lease so a
  passive HA node does not double-fire; rerun's per-run `Acquire` is the backstop.

## Alternatives considered

- **In-memory retry in the webhook worker.** Covers a flaky webhook but not a crash
  mid-escalation, and it does not make the two-layer story true. It leaves the
  exact failure (a lost alert after a restart) that motivated this.
- **Durable from the instant of the stop (synchronous enqueue).** Rejected: it puts
  a `store.Create` on the enforcement path, violating the hard "never waits" rule
  that protects the meter.
- **A connector catalog.** Rejected: it is where leash stops being small enough to
  audit. The command hook gives users the same reach without leash carrying it.

[ADR-0003]: 0003-at-most-once-counting.md
[ADR-0004]: 0004-one-dependency.md
