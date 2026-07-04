# Changelog

All notable changes to leash are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims
to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html) once it
reaches 1.0 (it is pre-1.0 and unstable until then).

## [Unreleased]

### Changed
- Updated the `rerun` dependency to v0.2.0 ("correct under failure") and adopted
  its typed error sentinels: `EnsureRun` and the append retry now match
  `rerun.ErrRunExists` / `rerun.ErrSeqConflict` with `errors.Is`, replacing a
  fragile SQLite error-string heuristic (so a resume no longer depends on the
  wording of a driver error). Existing ledger databases are migrated in place by
  rerun's versioned schema migrations on first open.

## [0.2.1] - 2026-07-04

### Added
- Request-level observability. A `leash_request_duration_seconds` histogram, a
  `leash_requests_in_flight` gauge, and `leash_responses_total{code}` counters
  (so a `503` is distinguishable from a boundary `429`), plus an `X-Request-Id`
  header on every response - propagated when safe, minted otherwise - that also
  appears in a per-request debug log.

## [0.2.0] - 2026-07-04

Following an adversarial architecture review, this release closes the
metering-integrity gaps. It is the first release to change default behavior.

### Changed (breaking)
- **`serve` requires authentication by default.** It refuses to start without
  `--auth-token` / `LEASH_AUTH_TOKEN`; pass `--insecure` to keep the old
  open-gateway behavior (which forwards live provider keys unauthenticated).
- **Runs are tenant-scoped when auth is on.** A run id is namespaced by the
  presenting credential, so two tenants using the same `X-Loop-Id` get separate,
  isolated budgets and neither can burn or read the other's run. `ps`/`inspect`/
  `kill` show the tenant-scoped ids.
- **Fail closed on unmeterable calls.** When a cost budget is active and leash
  cannot meter a call, it now refuses by default instead of forwarding it at $0.
  An unrecognized endpoint is rejected with 402; a known-provider call that
  returns unreadable usage stops the run (reason `meter_blind`). Restore the old
  behavior with `--on-blind=warn` (or `allow`). Set via `LEASH_ON_BLIND`.
- **The rate limit is now transient backpressure, not a terminal stop.** A
  rate-limited call is refused with a `Retry-After` header and the run keeps
  running, resuming once the trailing window decays - where before it stopped the
  run for good. Every other boundary still stops the run permanently.

### Fixed
- The blind-meter warning now goes through the structured logger, so it no longer
  emits a stray non-JSON line under `--log-format json`.
- Removed a stray `prices.json` demo artifact from the repository root and
  gitignored it (price tables are user-supplied and should not be committed).
- **Unbounded in-memory growth.** Idle runs are now evicted whether or not they
  have stopped (a running-but-idle run, or a rotated `X-Loop-Id`, was never
  freed), and per-run rate samples are pruned to the rate window (and not kept at
  all when no rate limit is set) instead of growing with every call. Memory is
  now bounded by the set of recently active runs.
- **Reasoning tokens were double-charged.** Reasoning tokens are a subset of the
  reported output tokens, but leash charged them at both the output and the
  reasoning rate, and double-counted them in the rate limiter. Now the
  non-reasoning output is charged at the output rate and the reasoning subset at
  the reasoning rate (or the output rate when none is set) - each token once.
- **Metering evasion / silent $0.** A usage block shaped for a different provider
  (e.g. an OpenAI body mis-tagged Anthropic via an `Anthropic-Version` header) is
  now treated as blind, not a real zero. The OpenAI Responses API (`/responses`,
  `input_tokens`/`output_tokens`, `output[]` text) is now metered instead of
  recording $0. `Unknown`-provider forwarded calls are flagged blind.
- **Client-disconnect metering bypass.** The call record is written with a
  detached context, so a client that drops the connection mid-stream can no
  longer prevent the call from being metered.
- **SQLite double-governor / split-brain.** rerun's SQLite lease is process-local,
  so two `leash serve` on one file both governed and double-spent. leash now takes
  an exclusive OS lock (`flock`) on a `<db>.govlock` sidecar (Unix), so a second
  governor is refused. `--standby` on a SQLite `--db` is now rejected up front
  (it requires Postgres) instead of silently providing no mutual exclusion.
- **Ledger writes now fail closed.** A run whose call record fails to persist is
  refused (503) on its next call until a write probe confirms the ledger
  recovered, instead of forwarding more unmetered spend. `EnsureRun` only treats
  a duplicate-key error as a resume, so a partial database failure surfaces
  rather than being mistaken for one. `/readyz` now exercises a write, so a
  read-only/full disk reports unready. New `leash_ledger_errors_total` metric.

### Added
- Soft limits and alerting. `--warn-at` (default 0.8) fires a one-time warning
  per run when a budget (cost, calls, deadline) crosses a fraction of its ceiling
  - a structured log, a `leash_budget_warnings_total{reason}` metric, and an
  observer event - so you can act before the hard stop. `serve --webhook URL`
  POSTs a JSON event on approach and on stop (best-effort, off the request path).
- `leash healthcheck` subcommand and a Dockerfile `HEALTHCHECK`, so the
  distroless image (no shell or curl) is health-checkable against the admin
  `/healthz`. The default image `CMD` enables the admin listener; the k8s and
  compose examples add `fsGroup`/`runAsNonRoot`, resource limits, and drain
  timing. The docker-publish workflow no longer moves `:latest` for prereleases.
- `--shutdown-timeout` (default 30s, was a hard-coded 5s) bounds how long
  graceful shutdown waits for in-flight streams, so long model streams are not
  severed on deploy. `--drain-delay` fails `/readyz` and pauses before draining
  so a load balancer can deregister the instance first.
- `--max-conns` caps simultaneous client connections, so a flood of slow or idle
  clients cannot exhaust file descriptors or goroutines.
- `--auth-token-file` reads auth token(s) from a file, keeping them off the
  process list and out of the environment.
- `serve` now warns when `--upstream` is plain `http` (the client credential
  would be forwarded in cleartext).
- Prompt-cache pricing. Price tables accept `cached_input` and `cache_write`
  rates, and leash reads cached-token counts from both providers (OpenAI's
  `prompt_tokens_details.cached_tokens`; Anthropic's `cache_read_input_tokens` /
  `cache_creation_input_tokens`), so a cache-heavy agent is metered to the
  invoice instead of over-counted. Omitted cache rates fall back to `input`.
- `--max-cost-per-call` (env `LEASH_MAX_COST_PER_CALL`) caps a single call's
  token cost; a call over the cap stops the run (reason `max_cost_per_call`),
  so a runaway large call cannot repeat. Bounds the one-call budget overshoot.
- Optional proxy authentication: `serve --auth-token` (prefer `LEASH_AUTH_TOKEN`)
  requires a matching `X-Leash-Token` header, compared in constant time and never
  logged or forwarded. Space-separate two tokens for zero-downtime rotation.
  `leash gen-token` prints a strong random token, and `--admin` `/metrics`
  requires the token when one is set.
- `serve --max-runs` caps the runs tracked in memory at once; a new run beyond
  the cap is refused 503, bounding a run-id-flood DoS.
- Per-subcommand `-h` help with a synopsis and examples.
- TTY-aware color for run status in `ps` and `inspect` and for the stop line
  (green running, amber stopped, red killed). It honors `NO_COLOR` and
  auto-disables for pipes, redirects, and `--json`.
- A workflow publishing the distroless image to GHCR
  (`ghcr.io/sylvester-francis/leash`) on each release tag.

### Changed
- CI gates on `gofmt` and `staticcheck -checks=all` via a new `make lint`; the
  tree passes both with zero findings.
- A nightly `govulncheck` workflow scans the code and dependency closure for
  known vulnerabilities.

### Fixed
- Landing page: the note arrow (`->`) no longer breaks across two lines under
  width pressure.

## [0.1.1] - 2026-07-03

Production hardening and release engineering.

### Added
- Request-path limits: `--max-body-bytes` (413 over the cap), run-id validation
  on the `X-Loop-Id` header and `--run` flag, server timeouts on both the
  gateway and the wrapper, a hardened upstream transport with
  `--upstream-header-timeout`, `serve --require-run-id`, and panic recovery.
- Native fuzz targets for the three parsers, with a `make fuzz` target.
- Structured logging with `log/slog`: `--log-level` and `--log-format`.
- `LEASH_`-prefixed environment fallbacks for every shared flag.
- `leash version` and version stamping via `-ldflags -X main.version`.
- `--json` output for `ps` and `inspect`.
- Admin listener `--admin` serving `/healthz`, `/readyz`, and `/metrics`, with a
  hand-rolled Prometheus text exposition and an `Observer` seam (no run-id
  labels).
- In-memory eviction of stopped, idle runs.
- PostgreSQL ledger backend selected by a `postgres://` DSN, and `serve
  --standby` for active/passive HA.
- Warm-path state cache making per-call overhead independent of journal size.
- Release tooling: Dockerfile, docker-compose demo, GoReleaser config, CI
  (build/vet/test/race/ascii/doc-check across Linux, macOS, and a Windows
  build), a nightly mutation-testing workflow, and a PostgreSQL CI job.
- A shareable landing page under `site/`, deployed to GitHub Pages.

### Changed
- `leash kill` now writes both the durable journal entry and a fast cancel flag
  so a warm governor observes a kill within one call.
- Child signal forwarding is guarded by GOOS; it is a no-op on Windows.

## [0.1.0] - 2026-07-03

### Added
- Initial application build: a durable agent spend governor. Six boundaries
  (kill, deadline, cost, calls, rate, stall) in a fixed order, a durable journal
  on rerun's store, OpenAI and Anthropic metering (JSON and SSE) with byte-exact
  stream teeing, the wrapper, the standalone gateway, and the `ps`, `inspect`,
  and `kill` commands.

[Unreleased]: https://github.com/sylvester-francis/leash/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/sylvester-francis/leash/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/sylvester-francis/leash/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/sylvester-francis/leash/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/sylvester-francis/leash/releases/tag/v0.1.0
