# Roadmap

This repository lands a single-PR sweep of **Phases 0 through 10** of the
NetMantle plan. Each phase has concrete shipped scope and explicit scaffolded
scope for follow-up hardening.

| Phase | Theme | Shipped now | Scaffolded / follow-up |
| ----- | ----- | ----------- | ---------------------- |
| 0 | Project foundation | Go module, CI, config loading, logging, SQLite migrations, auth/RBAC, metrics, Docker + Helm | Production hardening docs (threat model, runbooks) |
| 1 | MVP backup | Inventory CRUD, SSH transport, builtin CLI drivers, git-backed config store, backup runs, embedded UI | Additional vendor coverage and transport hardening |
| 2 | Change management & notifications | Diff engine, change events, webhook/Slack/email channels + rules | Policy tuning and richer routing/escalation controls |
| 3 | Auditing & search | SQLite FTS5 search, saved searches, CSV export | Advanced indexing and large-scale search tuning |
| 4 | Configuration compliance | Rules/rulesets/findings with transition notifications, **ISP/Cisco/MikroTik baseline rule packs** | Broader rule pack library and richer remediation workflows |
| 5 | Discovery & NMS sync | TCP/banner scan and NetBox JSON import | SNMP enrichment and LibreNMS/Zabbix synchronization |
| 6 | Push/pull automation | Push-job CRUD, template rendering, preview, grouped results, **per-driver `Apply()` live execution via transport routing** | — |
| 7 | In-app CLI & distributed pollers | Web terminal transcript/audit, poller registration + heartbeat, **poller job queue** (`poller_jobs` schema, idempotent enqueue/claim/complete/reclaim), **gRPC wire-protocol contract** (`.proto` + Go service interface), **wire-level poller core adapter** (authenticate/claim/report with tenant-verified claim + ownership checks), **dedicated gRPC+mTLS listener shell** | Full poller RPC registration + remote execution hardening |
| 8 | Runtime state auditing & compliance | Probe framework and runtime checks | Broader probe library and policy packs |
| 9 | Multi-tenancy & HA | Tenant CRUD, quotas, leader-elected scheduler, Helm chart, **extended HA failover tests** (split-brain prevention, leader handoff timeliness, rapid leadership-change stability) | Automated scale testing and HA chaos framework |
| 10 | Hardening + modern transports + topology + GitOps mirror | NETCONF helpers, LLDP/CDP topology API + UI canvas renderer, GitOps mirror, signed release + SBOM workflow, **NETCONF-over-SSH drivers hardened** (`cisco_netconf`, `junos_netconf`), **RESTCONF + gNMI native transports wired** (`restconf`, `gnmi`), **SSH known-hosts persistence** (closes threat-model T7), **DBKnownHostsStore failover validation suite**, **webhook/Slack URL sealing** (closes T7+T10), **topology API versioning** (`api_version`, `node_count`, `edge_count`, `?limit=`) | Full poller RPC registration/hardening |

## Immediate next actions

### Documentation expansion

- Keep this roadmap explicit about **shipped vs scaffolded** scope per phase.
- Add and maintain reviewer-oriented overviews in:
  - `/ARCHITECTURE.md`
  - `/DRIVERS.md`
  - `/SECURITY.md`

### Hardening core features

- Stabilize API contracts and storage formats before the first tagged stable
  release.
- Add migration/versioning guidance for SQLite schema changes:
  - migration files are append-only under `internal/storage/migrations`
  - never edit historical migrations after release
  - pair each schema change with repository/service compatibility notes
  - document rollback expectations before landing migration PRs

### Driver coverage

- ~~Add additional widely used vendors~~ ✅ Shipped: Fortinet FortiOS,
  Palo Alto PAN-OS, and Huawei VRP are now hardened CLI drivers in
  `internal/drivers/builtin/builtin.go` (see `/DRIVERS.md`).
- Hardened CLI coverage now spans Cisco IOS/NX-OS/IOS-XR, Arista EOS,
  Juniper Junos, MikroTik RouterOS, Nokia SR OS, Huawei VRP, Fortinet
  FortiOS, Palo Alto PAN-OS, BDCOM, V-SOL, and DBC.
- Keep status explicit in `/DRIVERS.md` as either **stub** or **hardened**.

### Testing & CI/CD

- ~~Add automated HA/failover test scenarios (leader election, scheduler handoff, tenant isolation under failover)~~ — **shipped**: `TestSplitBrainPrevention`, `TestLeaderHandoffTimeliness`, `TestMultipleRapidLeaseFlips`, `TestLeaseContextCancellation` are now in `internal/scheduler/scheduler_test.go`.
- Add integration tests for backup/diff/compliance end-to-end workflows.
- Keep `release.yml` producing signed artifacts + SBOM on each tag and extend
  release verification checks as needed.

### Topology & UI

- ~~Treat Phase 10 topology support as **minimal API-first** (not production UI)~~ — the topology canvas renderer is shipped. The API now returns `api_version`, `node_count`, `edge_count` fields (schema v1.0) and supports `?limit=`.
- Remaining: harden the graph canvas for very large topologies (>500 nodes); add incremental layout.

### Rule packs

- ~~Compliance rule packs~~ — **shipped** in `internal/compliance/rulepacks/` with `isp-baseline`, `cisco-ios-cis`, `mikrotik-baseline`.
- Remaining: Junos/Nokia/Huawei baseline packs; pack auto-update on version bump; UI picker.

### Community & contribution

- Keep contributor/security guidance current in `CONTRIBUTING.md` and
  `SECURITY.md`.
- Track scaffolded features as issues so progress is visible to reviewers.

## Strategic milestones

### Phase 11+ (future work)

- Harden model-driven transports for broader vendor/path coverage.
- Build a driver SDK registry for community contributions.
- Implement multi-tenant HA testing and scaling scenarios.

### 1.0‑RC1 milestone — reached ✅

All Phases 0–10 are landed. The V1 API surface is now frozen:

- **Native transports shipped:** RESTCONF (HTTPS, Basic/Bearer), gNMI
  (gRPC/TLS with JSON‑IETF), NETCONF‑over‑SSH.
- **gRPC+mTLS distributed‑poller listener** available in
  `internal/server/grpc.go`, wired into the main process lifecycle.
- **Full driver pack hardened:** Cisco IOS/NX‑OS/IOS‑XR, Arista EOS, Juniper
  Junos, MikroTik RouterOS, Nokia SR OS, Huawei VRP, Fortinet FortiOS, Palo
  Alto PAN‑OS, BDCOM, V‑SOL, DBC.

Remaining hardening items tracked as explicit follow‑ups below; they do not
block the 1.0‑RC1 tag.

### Post‑RC1 follow‑ups

- ~~**Automation `Apply()` live execution** — per‑driver push path (Phase 6).~~ ✅ Partially shipped: SSH/CLI transport executor wired in `cmd/netmantle/main.go`. RESTCONF, gNMI, and NETCONF transports currently only expose read (get-config) paths — applying rendered templates via those transports is a post-RC1 follow-up.
- **RESTCONF / gNMI / NETCONF `Apply()` paths** — write/edit-config operations for model-driven transports (currently read-only for backup/capture).
- ~~**HA chaos/scale validation** — gRPC session chaos tests, 1 000+ concurrent
  device scale validation (Phase 9 follow‑up).~~ ✅ Shipped: chaos coverage now
  includes poller wire flap/timeout behavior in `internal/transport/chaos_test.go`,
  gRPC graceful-stop/in-flight handling in `internal/server/grpc_test.go`, and
  1,000-poller/10,000-queue stress validation in `internal/poller/scale_test.go`.
- **Additional rule packs** — Junos/Nokia/Huawei compliance baselines, UI
  picker for pack selection.
- **Full poller RPC hardening** — complete remote execution and registration
  over the gRPC wire protocol.
