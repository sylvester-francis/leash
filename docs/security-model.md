# Security model

What leash defends, what it does not, and where the trust boundaries sit. Stated
plainly so an operator can place leash correctly in a network.

## The trust boundary

By default leash trusts the network segment between the agent and the proxy:
with no token configured, anyone who can reach the listener can spend under any
run id. Do not rely on that alone on an untrusted network.

Mitigations, in order of leverage:

- **Authenticate.** `--auth-token` (prefer the `LEASH_AUTH_TOKEN` environment
  variable so the secret stays out of the process list) requires every request
  to carry a matching `X-Leash-Token` header, so reaching the listener is not
  enough to spend. The token is compared in constant time and never logged or
  forwarded upstream. Generate a strong one with `leash gen-token`. leash does
  not authenticate identities per caller - that belongs at an ingress or service
  mesh (mTLS, OIDC) in front of leash, which it composes with.
- **Rotate the token.** It is a static shared secret with no expiry; rotate it
  periodically and on any suspected leak. Configure two tokens (space-separated)
  for a zero-downtime overlap: accept the old and new token, roll clients to the
  new one, then drop the old.
- **Bound the blast radius.** `--max-runs` caps the number of runs tracked in
  memory at once; a new run beyond the cap is refused 503, so a flood of run ids
  cannot exhaust memory or the ledger.
- `--require-run-id` refuses any request without an `X-Loop-Id`, closing the
  shared-gateway footgun where one stopped `default` run would 429 all untagged
  traffic, or where untagged traffic pools into one budget by accident.
- Network ACLs (security groups, firewall rules, a private subnet) that limit who
  can reach the proxy listener.
- Put the `--admin` listener on a separate address and segment from the proxy;
  when a token is set, `/metrics` requires it too, while `/healthz` and `/readyz`
  stay open for orchestrator probes.

## Secrets and data at rest

leash holds no secrets at rest. It forwards the client's `Authorization`,
`x-api-key`, and `api-key` headers upstream untouched, so the provider
authenticates exactly as it would with leash out of the path, and it never logs
or persists those headers. It never persists request or response bodies.

What the ledger persists, and only this:

- usage numbers the wire reported (input, output, reasoning tokens),
- a content-fingerprint hash (used by the stall boundary; not the content),
- timestamps,
- stop reasons.

The redaction is structural, not a scrubbing pass: bodies and headers are never
handed to the ledger in the first place. `Ledger.RawLogs` exposes the exact bytes
on disk, and a test asserts no secret or body ever appears there. The
secret-never-logged path is tested at debug level in JSON format.

## Not an open proxy

The upstream is operator-set with `--upstream` (or inferred to the OpenAI and
Anthropic hosts). A client cannot steer leash at an arbitrary destination, so
leash cannot be used as an open forward proxy. Run ids that enter from outside
(the `X-Loop-Id` header and the `--run` flag) are validated against a strict
pattern, which also blocks log injection via header newlines.

## Fail closed

If the ledger is unavailable, leash refuses the call with a 5xx rather than
forwarding it unmetered. A call that cannot be accounted for is not made. The
request-body read is capped (`--max-body-bytes`, default 10 MiB) so a hostile or
buggy client cannot exhaust memory, and the request path recovers from any panic
into a 500 rather than taking the gateway down.

## What leash does not defend against

- A malicious operator. Anyone who can run `leash kill`, read or write the `--db`,
  or change `--upstream` controls the governor. Restrict those.
- An agent with its own provider key that talks to the provider directly,
  bypassing the proxy. leash governs only the traffic that flows through it; it
  cannot stop spend on a path it never sees. Do not hand the agent a usable key
  for the real endpoint if the proxy is meant to be the only route.
- Network eavesdropping between the agent and the proxy. leash does not terminate
  TLS in v0.1; terminate it at your ingress.

## Supply chain

leash has one direct dependency (rerun) and a small indirect closure (a pure-Go
SQLite driver, a pure-Go Postgres driver, and their support libraries); there is
no C toolchain and no provider SDK. A nightly `govulncheck` run scans the code
and that closure for known vulnerabilities, and CI builds with `check-latest`
so a patched Go toolchain is picked up automatically. Release binaries are
CGO-free static builds with reproducible `-trimpath` flags, published with a
`checksums.txt`.

## Hardening checklist

- Set `--require-run-id` on a shared gateway.
- Put `--admin` on a separate address and segment; do not expose it publicly.
- Restrict who can run `leash kill` and who can read or write the `--db`.
- Restrict who can set `--upstream` and start `leash serve`.
- Terminate TLS at the ingress; keep the proxy listener private.
