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
| 1 | Inventory CRUD, SSH transport, Cisco IOS / Arista EOS / Junos / MikroTik / generic-SSH drivers, git-backed config store, BackupNow + run history, embedded web UI, OpenAPI + Swagger UI |
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

## Architecture

See [docs/architecture.md](docs/architecture.md) and the ADRs under
[docs/adr/](docs/adr).

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `make lint test` before pushing.

## License

Apache-2.0. See [LICENSE](LICENSE).
