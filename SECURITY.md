# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately, not in a public issue. Use
GitHub's private security advisories
(https://github.com/sylvester-francis/leash/security/advisories/new), or email
the maintainer at sylvesterranjithfrancis@gmail.com. Include a description, the
affected version, and reproduction steps. You will get an acknowledgement within
a few days.

leash is pre-1.0 and unstable; there is no long-term-support branch. Fixes land
on the default branch and in the next tagged release.

## Trust model

leash assumes a trusted network segment between the agent and the proxy: anyone
who can reach the listener can spend under any run id, because there is no auth
layer on the proxy by design (access control is the network's job). Mitigate
with `--require-run-id`, network ACLs, and by putting the `--admin` listener on
a separate segment. leash holds no secrets at rest: it forwards `Authorization`,
`x-api-key`, and `api-key` upstream untouched and never logs or persists them,
and it never persists request or response bodies. The upstream is operator-set
via `--upstream`, so leash is not an open proxy. If the ledger is unavailable,
leash fails closed: the call is refused with a 5xx, never forwarded unmetered.
leash does not defend against a malicious operator, or against an agent that has
its own provider key and talks to the provider directly, bypassing the proxy.
See `docs/security-model.md` for the full statement.
