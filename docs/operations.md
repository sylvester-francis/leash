# Operations runbook

Running leash in production: backups, sizing, monitoring, failover, and the
honest state of retention.

## Backups (SQLite)

The SQLite ledger runs in WAL mode: writes go to a `-wal` sidecar file and are
folded into the main `.db` on a checkpoint. A correct backup captures a
consistent point, not a half-applied WAL.

Two safe options:

- Checkpoint, then copy. Ask SQLite to fold the WAL into the main file, then copy
  the `.db`. With no leash-native checkpoint command, the simplest reliable path
  is to stop the governor briefly (below), or use the `sqlite3` CLI's
  `PRAGMA wal_checkpoint(TRUNCATE);` against the file while no governor is
  writing.
- Stop and copy. Stop the single governor process, then copy all three files
  together: `leash.db`, `leash.db-wal`, and `leash.db-shm`. Restart the governor;
  it folds the journal and resumes every budget.

Do not copy a live `.db` alone: without its `-wal` the copy can miss recent
appends. For Postgres, use your normal Postgres backup tooling (`pg_dump` or a
base backup); the ledger is ordinary tables.

## Sizing the ledger

Journal rows are append-only and small: usage numbers, a fingerprint hash, a
timestamp, and stop reasons. Measured growth on SQLite is about **290 bytes per
governed call** (main `.db` file, marginal, on an Apple M4 Pro with go1.25.6;
your bytes-per-call will vary a little with model-name length).

Size a month from your call rate:

```
bytes/month = calls/second * 86400 * 30 * 290

# Example: a steady 1 call/second
1 * 86400 * 290       ~= 25 MB/day
1 * 86400 * 30 * 290  ~= 0.75 GB/month
```

At 10 calls/second that is about 7.5 GB/month. Provision disk for the retention
window you need and watch free space (see below), because leash does not prune
(also below).

## Monitoring and alerting

Scrape the `--admin` `/metrics` endpoint. The series worth alerting on:

- `up` / `GET /readyz` returning 503. A 503 means a ledger read failed within the
  1s budget: the governor cannot account for calls and is failing closed. Page on
  it.
- `rate(leash_upstream_errors_total[5m])`. A rising rate means calls are failing
  to reach or read the upstream.
- `increase(leash_stops_total{reason="..."}[...])`. Watch which boundaries fire.
  A spike in `cost_budget` or `max_calls` stops may mean a runaway agent; a spike
  in `stall` means agents repeating themselves.
- `leash_active_runs`. Unexpected growth on a shared gateway can indicate clients
  not reusing run ids (each new `X-Loop-Id` is a new run).
- `leash_blind_calls_total`. A climbing count means calls are being metered blind
  (no usage on the wire), so cost and rate boundaries cannot act on them; check
  `--prices` and that `stream_options.include_usage` injection is on.
- Free disk on the ledger volume, sized against the growth arithmetic above.

The series carry no run-id labels by design (unbounded cardinality). Per-run
detail comes from `leash ps` and `leash inspect` against the same `--db`.

## Standby failover

With a Postgres ledger the governance lease is a cross-process advisory lock, so
exactly one instance governs at a time. Run a warm standby:

```sh
# primary
leash serve --listen :8088 --admin :9090 --db postgres://user:pass@db/leash --max-cost 20
# standby, same ledger
leash serve --listen :8088 --admin :9090 --db postgres://user:pass@db/leash --max-cost 20 --standby
```

The standby waits on the lease and logs that it is standing by. When the primary
exits (or its connection drops, releasing the advisory lock), the standby
acquires the lease within its retry interval and begins governing. Because the
ledger is the source of truth, the standby resumes every run's budget exactly
where the primary left off. Front both instances with a load balancer or a
floating address so clients follow the active one.

SQLite standby only makes sense within one host (its lease is process-local), and
the rule there is one governor per SQLite ledger; for real failover across hosts,
use Postgres.

## Retention and pruning

Journal rows are permanent. rerun's `Store` interface exposes no delete or
retention API, and leash will not work around that with direct SQL: doing so
would break the storage abstraction and the Postgres backend, and risk corrupting
the very account leash exists to protect. Journal retention is future work,
pending an upstream extension to rerun's interface.

Until then:

- Size disk with the growth arithmetic above; ledger rows are small.
- To bound total size, rotate to a fresh `--db` for a new epoch (a new day, a new
  release) and archive the old file. Finished runs already drop off `leash ps`;
  rotation is how you retire their storage.

This is stated plainly rather than hidden: there is no supported prune today, and
no direct-SQL hack is shipped in its place.
