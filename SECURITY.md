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

## Current limitations

- Project is still pre-stable; APIs and storage formats are not yet frozen.
- NETCONF/RESTCONF/gNMI driver paths are scaffolded and need transport/security
  hardening before production use.
- Additional security hardening work is tracked in roadmap follow-ups
  (threat-model docs, pen-test depth, expanded operational runbooks).
