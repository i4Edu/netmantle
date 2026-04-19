# NetMantle

> Self-hosted, vendor-agnostic Network Configuration Management (NCM) and
> automation platform — an open alternative to commercial NCM tools.

NetMantle backs up, version-controls, audits, and automates network device
configurations. It is API-first, GitOps-friendly, and built to be deployed on
your own infrastructure.

This repository contains a single-PR landing of **Phases 0 through 10** of
the NetMantle plan. Each phase ships in MVP form — production-grade
hardening (NETCONF/RESTCONF/gNMI wire formats beyond stubs, a topology UI,
a driver-SDK registry, additional vendor drivers, full HA testing) is
explicitly follow-up work. See [docs/roadmap.md](docs/roadmap.md) for
what shipped vs. what is still scaffolded.

What's in the box:

| Phase | Capability |
|-------|------------|
| 0 | Foundation: Go module, CI, config, logging, SQLite + migrations, envelope-encrypted secrets, local auth + RBAC, Prometheus metrics, Docker image, Helm chart |
| 1 | Inventory CRUD, SSH transport, ~20 device drivers ([driver matrix](DRIVERS.md)) including Cisco IOS / IOS-XR / NX-OS, Arista EOS, Junos, MikroTik, Nokia SR OS, Huawei VRP, FortiOS, Palo Alto PAN-OS, plus generic SSH and ONT/OLT drivers; git-backed config store, BackupNow + run history, embedded web UI, OpenAPI + Swagger UI |
| 2 | Diff engine with platform-aware ignore rules, ChangeEvent recording, notification channels (webhook / Slack / SMTP) and rules |
| 3 | SQLite-FTS5 search across all stored configs, saved searches, CSV export of changes |
| 4 | Compliance rule engine (regex / must-include / must-exclude / ordered-block), findings, transition-only notifications |
| 5 | TCP/banner port-scan discovery → driver fingerprinting; NetBox JSON importer |
| 6 | Push-job CRUD, `text/template` rendering, preview, smart result grouping |
| 7 | RFC-6455 WebSocket in-app web terminal with full audit transcript; poller registration with bcrypt-hashed bootstrap tokens |
| 8 | Probes framework + reuse of the rule engine for runtime compliance, with a leader-elected retention pruner |
| 9 | Tenant CRUD + per-tenant device quotas, DB-row leader-elected scheduler, Helm chart |
| 10 | NETCONF helpers + RESTCONF/gNMI stub drivers, LLDP/CDP topology graph, GitOps mirror to external git, `release.yml` workflow with cosign signing + syft SBOM |

## Status

⚠️ Early development. APIs and storage formats may change before the first
tagged release. Do not yet rely on it for production.

## Screenshots

A short tour of the embedded web UI and operator endpoints. The UI ships
with a modern admin-style theme — a sticky light top bar with a gradient
brand mark, a deep slate sidebar with pill-style active items, soft
shadowed cards, an Inter-led system font stack, and matching light/dark
modes. It is built with vanilla JS + hand-rolled CSS (no framework, no
build step, no third-party CSS); see
[docs/ui-style-guide.md](docs/ui-style-guide.md) for the design tokens
and [docs/user-guide.md](docs/user-guide.md) for step-by-step walkthroughs.

> The screenshots below were captured against the current theme. Sections
> that haven't been refreshed yet (Backups, Topology, Approvals, Device
> detail, Prometheus metrics) are marked inline.

### Sign-in

The bootstrap admin password is printed once on first start, or pre-seeded via
`NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD`. The sign-in card now carries the
brand mark, a tagline, and a hint pointing to the bootstrap-password
environment variable.

![Sign-in screen](https://github.com/user-attachments/assets/6621033a-3e16-449d-95e1-18ae6e02a75c)

### Dashboard

Single-round-trip landing page: device totals with a 7-day delta, current
compliance %, 24h backup success rate with a 14-day sparkline, the approvals
queue, status-by-driver bars, drift hotspots, a recent-events timeline, and a
pollers/git-mirror health card. All sections are best-effort — a sub-query
failure zeroes that field but the response stays 200.

![Dashboard with stat tiles, status-by-driver bars, recent events, and health card](https://github.com/user-attachments/assets/a54c9c19-7b6e-410a-96b3-dedd1bf1f376)

### Empty dashboard (first login)

Two-pane Inventory layout: the left rail lists devices and exposes the
**Add device** and **Add credential** forms; the right pane shows device detail.

> Pending a refresh against the current theme.

![Empty dashboard after first login](https://github.com/user-attachments/assets/50200d85-ac6d-4f03-ae93-56568e3ffc80)

### Devices list with the Add device form open

The driver dropdown is populated dynamically from the registered drivers
(Cisco IOS / IOS-XR / NX-OS / NETCONF, Arista EOS, Junos CLI/NETCONF,
MikroTik RouterOS, Nokia SR OS, Huawei VRP, FortiOS, Palo Alto PAN-OS,
gNMI, RESTCONF, generic SSH, and several ONT/OLT drivers — BDCOM, DBC, VSOL).

![Devices list with Add device form expanded](https://github.com/user-attachments/assets/c91da805-748a-4505-80d4-fb49e1618af7)

### Device detail

Clicking a device shows the latest stored configuration, recent run history,
and **Backup now** / **Delete** controls.

> Pending a refresh against the current theme.

![Device detail pane](https://github.com/user-attachments/assets/3b0b6939-0ad1-4dcc-8c01-b81183cce93c)

### Backups & changes

Recent change events are listed on the left; clicking one shows the unified
diff and a **Mark reviewed** action.

> Pending a refresh against the current theme.

![Backups view with diff pane](https://github.com/user-attachments/assets/99260c71-3ead-4bb0-9fb4-9320c0ba660c)

### Compliance

Define rules (`must_include`, `must_exclude`, `regex`, `ordered_block`) with
severities, and review findings produced when backups are evaluated.

![Compliance rules table](https://github.com/user-attachments/assets/9d7461c5-2a23-41ed-a465-546928797978)

### Approvals

Change requests for push jobs flow through `draft → submitted → approved →
applied`, with reviewer notes captured on each transition.

> Pending a refresh against the current theme.

![Approvals queue](https://github.com/user-attachments/assets/87733faa-6ce0-40d2-8f03-4ee3fbbe6a9b)

### Topology

LLDP/CDP neighbour reports stored as `neighbors` probe runs are merged into a
deduplicated link list. A graph canvas renderer is tracked as follow-up work.

> Pending a refresh against the current theme.

![Topology nodes and links table](https://github.com/user-attachments/assets/a0eaa505-7565-4034-90e2-5f5779c20f44)

### Audit log

A filterable view of every state-changing API call, including the actor,
source (`ui` / `api` / `scheduler` / `poller` / `system`), action, and target.

![Audit log with filters](https://github.com/user-attachments/assets/e75a46f8-3170-4fb4-b8fe-f7b65c8f3ccb)

### Settings

Tenants, API tokens, notification channels and rules, and registered pollers
are each listed in a dedicated card.

![Settings cards](https://github.com/user-attachments/assets/41e43169-a890-4aba-9041-fe0c1d2eccf0)

### Prometheus metrics

`/metrics` exposes Go runtime metrics plus NetMantle-specific counters
(uptime, HTTP request totals and latency histograms, backup outcomes, …) ready
to be scraped by Prometheus.

![Prometheus metrics endpoint](https://github.com/user-attachments/assets/256feb6a-3a44-41ea-817b-6f206a67da11)

## Quickstart

```bash
# Build
make build

# Initialize a local data dir and run with default config
mkdir -p data
./bin/netmantle serve --config config.example.yaml
```

The server listens on `:8080` by default. On first start, an `admin` user is
created and its randomly generated password is printed to the log **once** —
capture it. You can also pre-seed credentials with:

```bash
NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-one' ./bin/netmantle serve
```

Then open <http://localhost:8080/> and log in.

### Docker

```bash
make docker
docker run --rm -p 8080:8080 -v $(pwd)/data:/var/lib/netmantle \
  -e NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-one' \
  ghcr.io/i4edu/netmantle:dev
```

## API

- OpenAPI spec: `GET /api/openapi.yaml`
- Interactive docs: `GET /api/docs`
- Health: `GET /healthz`, `GET /readyz`
- Metrics (Prometheus): `GET /metrics`

Authenticate by `POST /api/v1/auth/login` and use the returned cookie for
subsequent requests.

## Documentation

- [User guide](docs/user-guide.md) — install, first login, day-to-day workflows
- [Architecture](docs/architecture.md) and the ADRs under [docs/adr/](docs/adr)
- [Roadmap](docs/roadmap.md) — what shipped vs. what is still scaffolded
- [Driver SDK](docs/driver-sdk.md)
- [Reviewer architecture summary](ARCHITECTURE.md)
- [Driver maturity matrix](DRIVERS.md)
- [Security policy](SECURITY.md)

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `make lint test` before pushing.

## License

Apache-2.0. See [LICENSE](LICENSE).
