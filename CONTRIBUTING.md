# Contributing

leash is a small, deliberately-bounded codebase. Changes are welcome when they
keep the guarantee honest and the build green.

## Requirements

- Go 1.25 or newer.
- No C toolchain: the SQLite ledger is pure Go via modernc.org/sqlite.
- The only direct dependency is github.com/sylvester-francis/rerun. Adding a
  third-party dependency to the core needs a strong reason.

## The green bar

Every change must keep all of these passing:

```sh
make build        # go build ./... and the leash binary
make vet          # go vet ./...
make test         # go test ./...
make ascii-check  # no non-ASCII byte in any .go or .md file
```

And the concurrent paths must stay race-clean:

```sh
go test -race ./...
```

## Conventions

- **Test first.** Boundaries and cost math are table-driven; new behavior comes
  with a test that failed before the change.
- **Pure ASCII everywhere**, in code and docs. `make ascii-check` enforces it.
- **Godoc on every exported symbol.** No exceptions.
- **Wrap errors with context** (`fmt.Errorf("...: %w", err)`), and never panic in
  a request path.
- **Count only what the wire reports.** The token meter must never estimate.
- **The ledger stores accounting only** - usage numbers, fingerprint hashes,
  timestamps, reasons. Never a request or response body, never a secret.

## Where things live

The layers depend inward toward the pure core; see the Design section of the
[README](README.md).

- `internal/policy` - the pure guarantee: cost math, state, boundaries. No I/O.
- `internal/meter` - reading provider wires (JSON and SSE).
- `internal/ledger` - the durable journal on rerun's Store.
- `internal/proxy` - per-call enforcement, streaming, header handling.
- `internal/wrap` - launching the child and mapping its exit code.
- `cmd/leash` - the CLI surface.

A change to the core should not need to touch the outer layers, and a change to
an outer layer should not reach into the core's internals.

## Mutation testing

The deterministic core is held to a measured mutation bar:

```sh
make mutate       # gremlins on internal/policy and internal/meter
```

Report a kill rate only when you have measured it. Never claim one you have not
run.

## Proposing a change

Substantial changes (a new feature, a new flag, a change to behavior,
architecture, or a public contract) start with an **Architecture Decision Record**,
not a pull request:

1. Copy an existing record in [`docs/adr/`](docs/adr/) to a new numbered file with
   status *Proposed*. Describe the failure mode the change addresses (leash's
   controls are each motivated by a way an agent loop goes wrong, so name the way
   it goes wrong), the options, and the decision.
2. Open the ADR as a pull request for discussion. Once it is approved, implement it
   and open the implementation pull request referencing the ADR.

Obvious small fixes (typos, docs, a clear bug with an obvious fix) can skip the ADR
and go straight to a focused pull request.

Keep pull requests focused, tested, and green. A PR that changes more than **20
files** is automatically blocked; split it into smaller PRs (an approved ADR can
carry the plan across them).

## Forks

leash is Apache 2.0, so you are free to fork and modify it. If your fork improves
leash, please send the improvement back as a pull request instead of letting it
diverge. Upstreaming keeps everyone on one maintained line and gets your change
reviewed and released. This is a request, not a license term.
