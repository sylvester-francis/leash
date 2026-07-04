# ADR-0006: Scope runs by the presenting credential

Status: accepted (added in v0.2.0)

## Context

A run is named by the client with an `X-Loop-Id` header. On a shared gateway with
a flat run namespace, any authorized caller could name another tenant's run id and
either exhaust its budget (a denial-of-service) or read its cost from the boundary
body (a disclosure). The run namespace needs isolation between tenants.

## Decision

When authentication is on, a run id is **namespaced by a fingerprint of the
presenting token**: the effective run key is `hash(token)[:8] + "-" + clientRunID`.
Two tenants using the same `X-Loop-Id` therefore map to two separate, isolated
runs, and neither can touch or read the other's.

The fingerprint is derived *after* authentication succeeds, from the presented
token; the token itself is never logged or stored. The mapping is deterministic,
so it survives a restart with no ownership table to persist. With auth off, runs
share one namespace (unchanged single-tenant behavior).

## Consequences

- Tenant isolation with no per-run ownership state and no coordination: the
  namespace is a pure function of the credential.
- `ps`, `inspect`, and `kill` operate on the tenant-scoped ids the operator sees.
- It is isolation, not identity: leash does not authenticate *who* a caller is;
  that belongs at an ingress or mesh. leash guarantees only that distinct
  credentials get distinct, non-interfering budgets.

## Alternatives considered

- **Store an owner token per run and reject non-owners.** Requires durable
  ownership state, and a restart raises "who owns this run" for an in-flight run.
  The deterministic namespace sidesteps that entirely.
- **Which configured token matched, as the key.** The constant-time auth compare
  deliberately does not reveal which token matched; hashing the presented token
  keeps that property while still distinguishing tenants.
