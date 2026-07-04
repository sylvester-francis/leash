# Changelog

All notable changes to leash are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims
to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html) once it
reaches 1.0 (it is pre-1.0 and unstable until then).

## [Unreleased]

Following an adversarial architecture review, this release closes the
metering-integrity gaps. It is the first release to change default behavior.

### Changed (breaking)
- **Fail closed on unmeterable calls.** When a cost budget is active and leash
  cannot meter a call, it now refuses by default instead of forwarding it at $0.
  An unrecognized endpoint is rejected with 402; a known-provider call that
  returns unreadable usage stops the run (reason `meter_blind`). Restore the old
  behavior with `--on-blind=warn` (or `allow`). Set via `LEASH_ON_BLIND`.

### Fixed
- **Metering evasion / silent $0.** A usage block shaped for a different provider
  (e.g. an OpenAI body mis-tagged Anthropic via an `Anthropic-Version` header) is
  now treated as blind, not a real zero. The OpenAI Responses API (`/responses`,
  `input_tokens`/`output_tokens`, `output[]` text) is now metered instead of
  recording $0. `Unknown`-provider forwarded calls are flagged blind.
- **Client-disconnect metering bypass.** The call record is written with a
  detached context, so a client that drops the connection mid-stream can no
  longer prevent the call from being metered.

### Added
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

[Unreleased]: https://github.com/sylvester-francis/leash/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/sylvester-francis/leash/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/sylvester-francis/leash/releases/tag/v0.1.0
