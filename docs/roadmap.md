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
| 4 | Configuration compliance | Rules/rulesets/findings with transition notifications | Expanded rule packs and richer remediation workflows |
| 5 | Discovery & NMS sync | TCP/banner scan and NetBox JSON import | SNMP enrichment and LibreNMS/Zabbix synchronization |
| 6 | Push/pull automation | Push-job CRUD, template rendering, preview, grouped results | Per-driver `Apply()` execution path (currently preview-only) |
| 7 | In-app CLI & distributed pollers | Web terminal transcript/audit, poller registration + heartbeat | Full gRPC poller wire protocol and remote execution hardening |
| 8 | Runtime state auditing & compliance | Probe framework and runtime checks | Broader probe library and policy packs |
| 9 | Multi-tenancy & HA | Tenant CRUD, quotas, leader-elected scheduler, Helm chart | Automated HA/failover validation and scale testing |
| 10 | Hardening + modern transports + topology + GitOps mirror | NETCONF helpers, RESTCONF/gNMI stubs, LLDP/CDP topology API builder, GitOps mirror, signed release + SBOM workflow | Full NETCONF/RESTCONF/gNMI backup wiring, topology visualization UI, transport-level hardening |

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

- Keep current hardened CLI coverage for Cisco/Arista/Junos/MikroTik (+ Nokia,
  BDCOM, V-SOL, DBC).
- Add additional widely used vendors in staged order:
  1. Fortinet
  2. Palo Alto
  3. Huawei
- Keep status explicit in `/DRIVERS.md` as either **stub** or **hardened**.

### Testing & CI/CD

- Add automated HA/failover test scenarios (leader election, scheduler
  handoff, tenant isolation under failover).
- Add integration tests for backup/diff/compliance end-to-end workflows.
- Keep `release.yml` producing signed artifacts + SBOM on each tag and extend
  release verification checks as needed.

### Topology & UI

- Treat Phase 10 topology support as **minimal API-first** (not production UI).
- Build a topology visualization UI to improve operator/reviewer usability.

### Community & contribution

- Keep contributor/security guidance current in `CONTRIBUTING.md` and
  `SECURITY.md`.
- Track scaffolded features as issues so progress is visible to reviewers.

## Strategic milestones

### Phase 11+ (future work)

- Harden NETCONF/RESTCONF/gNMI drivers beyond stubs.
- Build a driver SDK registry for community contributions.
- Implement multi-tenant HA testing and scaling scenarios.

### First tagged stable MVP

- Target a stable end-to-end slice: inventory, backup, diff, compliance, and
  push-jobs.
- Lock API contracts and storage formats before tagging.
