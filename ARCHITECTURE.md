# ARCHITECTURE

This document is a reviewer-focused summary of current architecture and
limitations. Detailed package-level design remains in
[`docs/architecture.md`](docs/architecture.md) and ADRs under [`docs/adr/`](docs/adr/).

## Current design choices

- **Modular monolith**: one `netmantle` binary with clear `/internal` module
  boundaries to enable future extraction.
- **API-first core**: REST API drives both automation and embedded UI.
- **SQLite-first persistence** with append-only SQL migrations in
  `internal/storage/migrations` (PostgreSQL target remains follow-up).
- **Git-backed configuration history** via `internal/configstore`.
- **Pluggable device access layer** via `internal/drivers` and
  `internal/transport`.

## How the core subsystems interact

```
HTTP Request
    │
    ▼
internal/api (server.go + handlers_phases.go)
    │  Auth/RBAC check via internal/auth (session cookie, role gate)
    │
    ├─► internal/devices      Inventory CRUD (SQLite: devices, device_groups)
    │
    ├─► internal/backup       Backup orchestration
    │       │  Connects via internal/transport (SSH) using internal/credentials
    │       │  Drives internal/drivers (CLI per-vendor) to fetch config text
    │       │  Persists artifact text in internal/configstore (per-device git repo)
    │       │  Writes backup_runs + config_versions rows to SQLite
    │       │
    │       └─► PostCommit hooks (wired at startup in cmd/netmantle/main.go):
    │               internal/changes  — records diff event in change_events
    │               internal/compliance — evaluates all tenant rules → findings
    │               internal/search   — indexes artifact text into FTS5 table
    │               internal/gitops   — pushes to configured remote mirror
    │
    ├─► internal/changes      Diff retrieval from configstore, change_events CRUD
    │
    ├─► internal/compliance   Rule/finding CRUD; OnTransition → notify dispatch
    │       └─► internal/notify  Webhook / Slack / email dispatch
    │
    ├─► internal/scheduler    Leader-elected job runner (scheduler_leases in SQLite)
    │       Leader fires: scheduled backups, probe execution, saved-search alerts
    │
    ├─► internal/tenants      Tenant CRUD + device-quota enforcement
    │       Quota checked on device create; uses tenant_quotas table
    │
    └─► internal/gitops       GitOps mirror: push config commits to external remote
            Config stored in gitops_mirrors table (token envelope-encrypted)
```

## RBAC model

Roles are stored per-user in the `users` table (admin / operator / viewer).
`internal/auth` validates the session cookie, resolves the role, and stores a
`*auth.User` in the request context. Handlers gate mutations behind
`requireRole("operator")` or `requireRole("admin")` middleware.

Tenant isolation is enforced at the query layer: every query against
tenant-scoped tables (devices, credentials, etc.) carries a `WHERE tenant_id=?`
predicate derived from the authenticated user's `TenantID`.

## SQLite schema stability policy (pre-v1)

Migration files live under `internal/storage/migrations/` and are embedded in
the binary at build time. The runner (`storage.Migrate`) applies them in
version-number order, tracking applied versions in `schema_migrations`.

Rules:
- Migration files are **append-only**. An already-released migration must not
  be edited after it ships.
- Each new migration PR must include: (a) compatibility notes explaining which
  existing data is affected, and (b) rollback expectations (typically: revert
  the PR; no data migration is needed because new columns are nullable or have
  sensible defaults).
- API endpoints that depend on new schema columns must be added or updated in
  the same PR as the migration.

## GitOps mirror flow

1. After a successful backup commit, the `gitops` PostCommit hook fires.
2. `internal/gitops` reads the mirror config from `gitops_mirrors` for the
   tenant (remote URL, branch, encrypted token).
3. The hook pushes the device's git repo to the configured remote, making all
   config history available to external GitOps tooling.
4. Token is envelope-encrypted at rest; the raw token never appears in logs or
   API responses.

## Notification dispatch flow

1. A ChangeEvent or compliance-transition triggers `internal/notify`.
2. The dispatcher looks up matching `notification_rules` for the tenant.
3. Each rule points to a `notification_channels` row (webhook / Slack / email).
4. The channel config JSON is fetched, decrypted where needed, and the payload
   dispatched synchronously (in the PostCommit context with a 30s timeout).

## What is production-ready vs scaffolded

- **Ready in MVP scope**: inventory, backup orchestration, config versioning,
  diff/change events, compliance engine, push-job execution, tenant-aware core,
  signed release artifacts + SBOM generation.
- **Scaffolded / partial**:
  - NETCONF/RESTCONF/gNMI drivers are registered but not hardened end-to-end.
  - Pollers support registration/heartbeat; full gRPC wire protocol is pending.
  - Topology support is API-first (LLDP/CDP graph builder) without full UI.
  - HA behavior exists (leader election) but requires deeper automated failover
    and scale validation.

## API and storage stability policy (pre-v1)

- Endpoints marked `x-stability: frozen` in `internal/api/openapi/openapi.yaml`
  have stable request/response contracts. Breaking changes require a new major
  API version.
- API routes and payloads for non-frozen endpoints can evolve until first stable
  tag.
- Migration files are append-only; existing released migrations must not be
  rewritten.
- Any schema evolution should include compatibility notes and test coverage in
  the same PR.
