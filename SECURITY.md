# Security Policy

## Reporting a vulnerability

Please do **not** open a public issue for security vulnerabilities.
Report vulnerabilities privately via GitHub Security Advisories:
<https://github.com/i4Edu/netmantle/security/advisories/new>

Please include:

- affected component(s)
- reproduction steps and/or a proof of concept
- impacted version(s) or commit(s)

This allows maintainers to reproduce the issue and coordinate fix + disclosure
timelines privately.

## Current security posture

- Credentials/secrets are envelope-encrypted at rest (`internal/crypto`).
- Auth uses hashed passwords and signed session cookies with RBAC checks.
- Release workflow signs artifacts (cosign keyless) and publishes an SPDX SBOM.

## Threat model (summary)

### Attack surfaces

| Surface | Risk | Mitigation |
|---------|------|------------|
| REST API | Unauthorized access to device inventory or configs | Session-cookie auth + RBAC; unauthenticated endpoints limited to `/auth/login` |
| Credential store | Plaintext SSH credentials leaked from DB | Envelope-encrypted at rest in `credentials` table; secrets never returned by API |
| Admin bootstrap token | Attacker uses bootstrap token to create admin account | Token is shown once on first startup; rotate by running `--reset-bootstrap` |
| SSH transport | MITM on device connections | Known-hosts enforcement is a follow-up; currently trust-on-first-use |
| Tenant boundary | Tenant A reads Tenant B's configs | All queries carry `tenant_id=?` predicate; enforced at the repo/service layer |
| GitOps mirror token | Git remote token exfiltrated from DB | Stored envelope-encrypted in `gitops_mirrors.secret_envelope` |
| Notification channel config | Webhook/Slack tokens in DB | Stored as JSON in `notification_channels.config`; callers should encrypt sensitive fields before storing |
| Release artifacts | Supply-chain compromise | All release binaries are cosign-signed (keyless, OIDC identity); SPDX SBOM published with every tag |

### Bootstrap token handling

When NetMantle starts with an empty database it creates a single admin user and
prints a one-time bootstrap password to stdout (or the log at `INFO` level).
This password is **not stored in plaintext** — only its bcrypt hash is kept in
the `users` table.

Operational guidance:

1. Capture the bootstrap password from the first-start log and store it in your
   secrets manager immediately.
2. Create a named admin account with a strong password and delete or disable
   the bootstrap account before exposing the service externally.
3. If the bootstrap token is lost, re-run `netmantle bootstrap-reset` to
   invalidate the existing admin user and generate a new token. This requires
   local filesystem access to the database.
4. In Kubernetes deployments, the bootstrap password is written to the pod logs
   on the first init; capture it via `kubectl logs` before the pod restarts.

## Current limitations

- Project is still pre-stable; APIs and storage formats are not yet frozen.
- NETCONF/RESTCONF/gNMI driver paths are scaffolded and need transport/security
  hardening before production use.
- SSH known-hosts verification is not yet enforced (trust-on-first-use); this
  is tracked as a follow-up hardening item.
- Notification channel `config` fields are stored as plain JSON; operators
  should treat the database file as a secret and restrict filesystem access.
- Additional security hardening work is tracked in roadmap follow-ups
  (threat-model docs, pen-test depth, expanded operational runbooks).
