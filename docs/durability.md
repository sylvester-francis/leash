# Durable accounting

A framework's max-steps setting lives in memory and resets when the process
restarts. leash's account does not. Every governed call, every kill, and every
stop is written to a durable journal before it can affect a decision, and a
run's totals are rebuilt by replaying that journal. A restart - clean or after a
crash - reproduces the exact same totals, so a budget cannot be reset by killing
the process and starting it again.

This guide is the ledger in depth: how the journal is stored, what each entry
holds, how a crash resumes without double counting, and how to read or kill a
run from another terminal.

## The core idea: the journal is the source of truth

The account on disk is authoritative. The running proxy holds an in-memory
`policy.State` - the current calls, costs, killed flag, and stop reason - but
that state is only ever a cache of the journal. If the proxy dies, the truth on
disk is untouched, and the next process rebuilds the cache by folding the journal
from the beginning.

Folding is deterministic. `Ledger.Load` reads the entries in sequence order and
replays them: each call record is folded into the running totals, a kill sets the
killed flag, a stop freezes the reason and the final costs. Reading the same
journal twice yields identical totals, because a call is counted exactly when its
entry exists - never zero times, never twice.

> What is persisted is not a running total but the result each completed call
> produced. With the per-call records in hand, any process rebuilds the same
> totals by replaying them.

## The backend: a rerun Store

The journal is stored on a `rerun.Store`. The default backend is SQLite, pure Go
through `modernc.org/sqlite`, so leash builds and runs as a single static binary
with no C toolchain. `Ledger.Open` creates the parent directory if needed and
opens (or creates) the database file. The SQLite backend panics on an open
failure, and `Open` recovers that into an ordinary error so a caller never sees a
panic.

The database lives at `$HOME/.leash/leash.db` by default. Point it anywhere with
`--db`:

```sh
leash --db ./run.db --max-cost 5.00 --prices prices.json -- python my_agent.py
```

rerun also has a Postgres backend. leash uses SQLite by default; Postgres is the
option when you need true cross-process leasing (see the last section).

## Journal entries and tags

Every run is created under one workflow named `leash`. Its journal is a sequence
of entries, each with an integer sequence number, a tag, an optional payload, and
a timestamp. There are three kinds, told apart by tag:

- **call-N** - one governed call. `N` is the call index, so the tags read
  `call-0`, `call-1`, and so on, in order, for readable inspection. The payload
  is a `policy.CallRecord`: the usage numbers the wire reported, a content
  fingerprint hash, and a timestamp. No request or response body is ever in it.
- **kill** - a durable kill, one per run. It carries no payload. A later fold
  sets the killed flag so the next governed call trips the kill switch.
- **stop** - the terminal stop, one per run. Its payload freezes the stop reason
  and the final totals (calls, token cost, compute cost, total cost) at the
  moment the boundary tripped. The caller records it exactly once.

Sequence numbers are allocated as `max(seq) + 1`: an append reads the journal,
takes the last entry's sequence plus one, and writes there. If two writers pick
the same position - for example a governed call and a concurrent `leash kill` -
one of them loses on a uniqueness constraint. The loser re-reads the journal and
retries at the new tail, up to a bounded number of attempts, so a concurrent
writer can never corrupt the sequence or overwrite an entry. Any error that is
not a sequence conflict is returned immediately.

## What a fold reconstructs

Two of the totals are handled differently on the way back, and the reason is
determinism:

- **Token cost** is rebuilt from scratch by folding the `call-N` records through
  the governor's prices. This is authoritative: the same records and the same
  price table always produce the same token cost.
- **Compute cost** is time-derived (elapsed wall-clock times the compute rate)
  and cannot be replayed deterministically, so on a stopped run it is read back
  from the frozen snapshot in the stop entry. Total cost on load is then the
  freshly folded token cost plus that frozen compute cost.

That split is why an over-budget run reads back as still over budget: the stop
entry preserves the exact reason and the frozen figures that tripped it.

## The crash-safety guarantee

The promise, and it is tested end to end in `internal/proxy/crash_test.go`: kill
the proxy uncleanly mid-run, start a new process on the same `--db`, and the run
resumes with its totals intact. An over-budget run stays stopped, and no entry is
double counted.

`TestCrashResumePreservesBudgetNoDoubleCount` runs a proxy with `MaxCalls: 2`,
makes two calls that pass and a third that trips `max_calls` and records a stop,
then abandons the proxy without a clean shutdown. A second proxy opens the same
database and folds the journal: the very next call is refused with 429, the
rebuilt state shows exactly two calls (not four), the stop reason is still
`max_calls`, and the upstream recorded exactly two forwarded calls - a refused
call never reaches it.

### At-most-once, never twice

A call is counted exactly when its journal entry exists, and the entry is
appended only after the upstream has responded. So the one window in which a
crash can lose a call is the narrow gap after the upstream responds but before
the append commits. A crash there undercounts by exactly one call - the response
the client already received is not billed - and it never re-spends: on restart
the missing entry is simply absent, and nothing replays it. leash errs toward
undercounting by at most one, never toward charging twice.

## Resuming a budget on purpose

The same mechanism lets you resume a run deliberately across invocations. Name a
run with `--run` and reuse the name:

```sh
leash --run nightly --db ./run.db --max-cost 5.00 --prices prices.json -- python my_agent.py
# later, same name and same db - the budget picks up where it left off:
leash --run nightly --db ./run.db --max-cost 5.00 --prices prices.json -- python my_agent.py
```

`EnsureRun` treats an existing run as a resume, not an error. It tries to create
the run; if the create fails but the journal is readable, the run already exists
and leash continues against it. The second invocation folds the first
invocation's entries first, so the account continues rather than starting over. A
run left without a `--run` name gets a random one and is not meant to be resumed.

## Privacy: what the ledger never stores

Privacy here is a property of what the journal holds, not a redaction pass bolted
on afterward. An entry contains usage numbers, a content fingerprint hash, a
timestamp, and - for a stop - a reason. That is all.

- No request or response bodies are ever written. The fingerprint is a hash used
  to detect identical responses in a row (the stall boundary), not the content
  itself.
- Authorization and api-key headers are forwarded to the upstream untouched and
  are never logged or persisted.

`RawLogs` exposes the exact bytes on disk so tests can confirm that only
accounting, never a body or a secret, was ever persisted.

## Operating the ledger

All three commands read from (or append to) the database at `--db`, and all of
them work whether or not a run is live.

### leash ps

`leash ps` lists the runs still active and folds each one's journal to show the
current account:

```console
$ leash ps
RUN   CALLS  TOKENS$  COMPUTE$  TOTAL$  STATUS   REASON
demo  4      0.10     0.00      0.10    stopped  cost_budget
```

A run that a boundary stopped stays on the list as `stopped` with its reason, so
you can see why it ended. A run that a wrapper finished cleanly is retired from
the active list and no longer appears.

### leash inspect <run>

`leash inspect <run>` folds the journal and prints a header - status, calls,
costs, and token totals - followed by the per-entry table in sequence order:

```console
$ leash inspect demo
run demo  status stopped  calls 4
cost   $0.10 tokens + $0.00 compute = $0.10
tokens in 4000  out 2000  reasoning 0

SEQ  TAG     WHEN                       DETAIL
0    call-0  2026-07-03T15:08:05-04:00  demo-model in=1000 out=500 reasoning=0
...
4    stop    2026-07-03T15:08:05-04:00  stop: cost_budget
```

Each `call-N` row shows its model and usage; the `stop` row shows the reason. The
table is the journal itself, decoded for reading.

### leash kill <run>

`leash kill <run>` appends a durable kill entry to the run's journal from any
process pointed at the same `--db`:

```console
$ leash kill demo
leash: kill recorded for run demo; it stops on its next call
```

The kill takes effect when the governing process next folds the journal, which is
on the run's next call: that call is refused with `kill_switch`, and every call
after it gets the same answer. `TestKillFromSecondHandleStopsGovernor` proves the
cross-process path - a kill written through a second ledger handle stops a
governor running against the same database.

## Multiple instances and one database

Two different questions hide here: can two processes read and write one journal,
and can two processes govern the same run at once.

Reading and writing across processes works with SQLite. A `leash kill` or a
`leash inspect` from another terminal sees the live journal and can append to it;
SQLite's write-ahead log carries the concurrent reads and writes, and the bounded
sequence-conflict retry keeps appends from colliding.

Governing the same run twice at once is what the lease guards. `Acquire` takes a
non-blocking lease on the run. With the SQLite backend that lease is
process-local: it stops one process from governing the same run twice, but it
does not coordinate across processes. Because it is process-local, a crash
releases it for free - there is nothing left holding it - so a restart can
re-acquire and resume. True cross-process mutual exclusion, where two proxies
must never govern one run simultaneously, needs the rerun Postgres backend.
