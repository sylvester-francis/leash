# Compatibility and stability

What leash promises to keep stable, and what it does not, so you can depend on the
right things. leash is pre-1.0 and unstable overall; this document says which
surfaces are already treated as contracts and which are free to change.

## Versioning

leash follows [Semantic Versioning](https://semver.org). It is pre-1.0, so the
public surface may still change; when it does, a breaking change is called out in
the [CHANGELOG](CHANGELOG.md) under a "Changed (breaking)" heading and lands on a
minor bump. Every release is signed and carries provenance (see
[docs/security-model.md](docs/security-model.md#verifying-a-release)).

## Surfaces treated as contracts

These are the interfaces other systems integrate against, so they change only with
a deprecation and a documented migration:

- **CLI flags and `LEASH_*` environment variables.** Adding a flag is additive.
  Removing or repurposing one is a breaking change: it is deprecated first, kept
  working for at least one minor release, and noted in the CHANGELOG.
- **The 429 boundary body.** The JSON shape (`error.type`, `error.reason`,
  `error.run`, the cost fields) and the set of `reason` strings are a machine
  contract. New reasons may be added; existing ones keep their meaning.
- **Exit codes.** `0` success, `2` usage error, `1` runtime error, `3` a boundary
  stopped a wrapped run, otherwise the child's own code. These are stable.
- **The run header** (`X-Loop-Id`) and the auth header (`X-Leash-Token`).
- **Prometheus metric names.** Additive: new series may appear; existing names and
  label keys are kept.

## Durability contract

- **The journal is forward-compatible.** A newer leash opens a database written by
  an older one; schema migrations are applied in place on first open (by `rerun`).
  Do not point an older leash at a database a newer one has migrated.
- Fold is deterministic, so replaying an existing journal reproduces the same
  totals across versions. Replay compatibility is exercised by the ledger tests.

## Not stable

- **The Go API.** leash is a binary, not a library. The `internal/` packages are
  not importable and their signatures change freely. See the FAQ on whether leash
  is a Go library.
- **Log line wording and formats** beyond the structured fields. Parse metrics or
  the boundary body, not human log text.
- **The wire shapes leash reads** from providers track the providers themselves;
  leash reads new usage fields as they appear.

## Platforms

- Built and tested on Linux and macOS (amd64 and arm64). Windows is build-only:
  child-signal forwarding is unsupported there, and the SQLite single-governor OS
  lock is not enforced (use one governor per ledger, or Postgres).
- Built with the current Go minor release; CI uses `check-latest` so a patched
  toolchain is picked up automatically.
