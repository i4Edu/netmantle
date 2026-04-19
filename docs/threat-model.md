# Threat model

This document is the long‑form companion to [`SECURITY.md`](../SECURITY.md).
`SECURITY.md` is the policy and short summary; this file enumerates the
threats, the controls in place today, and the gaps explicitly tracked as
follow‑up work.

> The honest framing: NetMantle is **pre‑1.0**. The mitigations below are
> what the code actually implements today. Items in the *Gap* column are not
> "in design" — they are real, exploitable shortcuts you must compensate for
> at deploy time until they are addressed in a tagged release.

---

## 1. Assets

| Asset | Where it lives | Why it matters |
|-------|----------------|----------------|
| Network device credentials | `credentials` table, envelope‑encrypted via `internal/crypto` | Direct privileged access to production network gear |
| Master passphrase / KEK | Operator‑provided env (`NETMANTLE_SECURITY_MASTER_PASSPHRASE`) | Decrypts every stored credential and mirror token |
| Session signing key | `security.session_key` (env or generated) | Forges any user's session if leaked |
| Device configuration history | Per‑device git repos under `storage.config_repo_root` | Plaintext configs may contain pre‑shared keys, BGP MD5, SNMP communities, etc. |
| Audit log | `audit` table | Audit trail of operator actions; integrity depends on DB/file access controls |
| GitOps mirror tokens | `gitops_mirrors.secret_envelope`, envelope‑encrypted | Push access to an external git remote |
| Notification channel config | `notification_channels.config` (JSON) | Webhook URLs, Slack tokens, SMTP creds (SMTP password is sealed; others are not — see §5) |
| Tenant boundary | `tenant_id` column on every multi‑tenant row | Cross‑tenant data leakage is a privacy / contractual incident |
| Bootstrap admin token | One‑time, log‑emitted at first start | Initial admin access |
| Release artifacts | GitHub Releases, signed with cosign keyless | Supply‑chain integrity for downstream operators |

## 2. Trust boundaries

```
[ Operator browser ]            HTTPS (terminate at proxy / ingress)
        │
        ▼
[ Reverse proxy / Ingress ]     ── trust boundary ──
        │
        ▼
[ netmantle process ]           in-process; same trust domain as DB + git repos
   │       │       │
   ▼       ▼       ▼
[ SQLite ] [ git repos ] [ outbound: SSH to devices, HTTPS to webhooks/Slack/SMTP/git remotes ]
```

- **Browser → proxy**: TLS is the operator's responsibility. NetMantle does
  not terminate TLS itself.
- **Proxy → netmantle**: assumed same security domain. If you bridge an
  untrusted network here, restrict it with mTLS or network policy.
- **netmantle → SQLite + git repos**: filesystem trust. Anyone with read
  access to the data volume can read all encrypted blobs **and** the git
  history (which is plaintext).
- **netmantle → devices / external services**: outbound only. For SSH,
  NetMantle performs in‑memory TOFU‑style host‑key pinning for the lifetime
  of the process, but does not currently persist pins in a durable
  `known_hosts` store.

## 3. Threat → mitigation matrix

| # | Threat | Likelihood / Impact | Mitigation today | Gap (tracked follow‑up) |
|---|--------|---------------------|------------------|--------------------------|
| T1 | Unauthenticated API access | High / High | Session cookie + bcrypt password auth; RBAC (`admin`/`operator`/`viewer`); anonymous routes are limited to `/api/v1/auth/login`, `/api/openapi.yaml`, `/api/docs`, `/metrics`, `/healthz`, `/readyz`, and the embedded UI under `/` | Per‑tenant API rate limiting |
| T2 | Privilege escalation across roles | Med / High | RBAC checked at handler entry; `*auth.User` injected via context | Periodic third‑party RBAC audit |
| T3 | Cross‑tenant data leak | Med / High | Every query scoped by `tenant_id = ?` at repo/service layer; tenant quota enforced on device create | Automated property tests for tenant isolation |
| T4 | Stored credential exfiltration via DB read | Med / High | Envelope encryption (`internal/crypto`); plaintext never returned by API | Hardware‑backed KEK (HSM/KMS) |
| T5 | KEK / master passphrase leak | Low / Critical | Sourced from env; never logged; never written to disk by NetMantle | First‑class rotation CLI; HSM‑backed KEK |
| T6 | Session token forgery / interception | Low / High | HMAC‑signed session cookies; key configurable; cookie name configurable; cookie set with `HttpOnly` + `SameSite=Lax` in code; `Secure` set when the request reaches the app via TLS | Proxy‑aware `Secure` flag (set unconditionally when fronted by trusted TLS terminator) — see §6 |
| T7 | SSH MITM during device backup | Med / High | Per‑device timeout; credential cleartext scoped to dial via `credRepo.Use(...)` | **Known‑hosts enforcement** — currently trust‑on‑first‑use |
| T8 | Plaintext configuration leak (git repos) | Med / High | Filesystem permissions on the data volume | Optional encrypted‑at‑rest repo storage |
| T9 | GitOps mirror token leak | Low / Med | Envelope‑encrypted in `gitops_mirrors.secret_envelope` | Token rotation reminder / expiry warning |
| T10 | Webhook / Slack token leak from `notification_channels.config` | Med / Med | SMTP password is sealed into `password_envelope`; webhook & Slack tokens are not yet sealed | Seal all sensitive notify channel fields by type |
| T11 | Bootstrap token capture from logs | Med / Med | Token is one‑time; logged at `WARN` only on first start; bcrypt hash stored, never plaintext | Out‑of‑band bootstrap token delivery |
| T12 | Audit log tampering | Low / High | Append‑only writes via `internal/audit`; rows include actor + timestamp | Cryptographic chaining / external WORM sink |
| T13 | Supply‑chain compromise of release artifacts | Low / Critical | Release workflow signs binaries with cosign keyless (OIDC); SPDX SBOM published per tag | Reproducible builds verification; signed Helm chart |
| T14 | Dependency vulnerability | Med / Med | Go modules pinned; CI fails on `go mod tidy` drift | Automated dependency vulnerability scan in CI |
| T15 | Denial of service via expensive backups | Low / Med | `backup.workers` cap; per‑device timeout | API‑level rate limiting; circuit breakers per device |
| T16 | NETCONF/RESTCONF/gNMI scaffolded paths used in production | Med / High | Stubs return clear "not implemented" errors; `DRIVERS.md` lists them as scaffolded | Full implementation (roadmap Phase 10 follow‑up) |
| T17 | Live push automation misuse | N/A today | Per‑driver `Apply()` is intentionally unimplemented; `internal/automation` returns "preview only" | Implement `Apply()` with explicit confirmation flow + dry‑run gate |

## 4. Authentication & authorisation

- **Sessions.** `internal/auth` issues HMAC‑signed cookies named per
  `security.session_cookie`. The signing key is `security.session_key` —
  pin it explicitly in HA deployments so all replicas accept the same
  cookies.
- **Passwords.** Stored as bcrypt hashes in the `users` table.
- **RBAC.** Three roles (`admin`, `operator`, `viewer`) checked at handler
  entry. New endpoints **must** declare a role.
- **API tokens.** `internal/apitokens` issues bearer tokens for machine
  callers; tokens are scoped per tenant.

## 5. Credential & secret handling

- The KEK is derived from `security.master_passphrase` by `internal/crypto`.
  Without it, encrypted blobs are unreadable by design.
- Device credentials are sealed on save, unsealed in `credRepo.Use(ctx, ...)`
  callbacks only, and the cleartext byte slice is scoped to the dial call.
  Do not regress this pattern.
- GitOps mirror tokens are sealed similarly into
  `gitops_mirrors.secret_envelope`.
- **Notification channels.** SMTP `password` is sealed into
  `password_envelope` (the plaintext field is removed before storage).
  Webhook and Slack tokens are stored as JSON in `notification_channels.config`
  *as provided*. Until §3 T10 is closed, restrict filesystem read access to
  the database file accordingly.

## 6. Cookies, transport, and headers

- NetMantle does not terminate TLS. Operators must front it with TLS.
- The API server sets `HttpOnly` and `SameSite=Lax` on the session cookie in
  code.
- The remaining gap is the `Secure` attribute: it is only set when the
  request reaches the app over TLS (`r.TLS != nil`). When TLS is terminated
  at a reverse proxy or ingress, the cookie will not carry `Secure` until
  proxy‑aware hardening lands. Until then, operators must ensure their
  deployment only exposes NetMantle behind HTTPS so the unflagged cookie is
  never sent over plaintext.

## 7. Multi‑tenancy

Every multi‑tenant row carries `tenant_id`. Repository/service code adds the
predicate to every query — there is no global "as admin" bypass for tenant
scoping. Tenant quotas (e.g. max devices) are enforced at create time by
`internal/tenants`.

When adding new tables: include `tenant_id INTEGER NOT NULL` and an index
that starts with it.

## 8. Logging & audit

- Structured logs via `slog`. Never log secrets — review every new `log.With`
  / `log.Info` call.
- The audit log records actor, action, target, and outcome. The API exposes
  query endpoints (see `internal/api/openapi/openapi.yaml`).
- Cryptographic chaining of audit rows is a follow‑up (T12).

## 9. Supply chain

- Container image is built from a distroless non‑root base.
- Releases are cosign‑signed (keyless / OIDC); verify with `cosign verify` in
  your deploy pipeline.
- An SPDX SBOM is published with every tag.
- Go modules are pinned via `go.sum`; CI fails on `go mod tidy` drift.

## 10. Reporting

Vulnerabilities go through GitHub Security Advisories — see
[`SECURITY.md`](../SECURITY.md). Do not file public issues for security bugs.
