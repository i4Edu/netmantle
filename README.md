# NetMantle

> Self-hosted, vendor-agnostic Network Configuration Management (NCM) and
> automation platform — an open alternative to commercial NCM tools.

NetMantle backs up, version-controls, audits, and automates network device
configurations. It is API-first, GitOps-friendly, and built to be deployed on
your own infrastructure.

This repository contains the **Phase 0 + Phase 1 MVP**: project foundation,
device inventory, encrypted credentials, SSH-based config backup with a Cisco
IOS driver, git-backed config versioning, REST API + OpenAPI, and a minimal
embedded web UI. Subsequent phases (change notifications, compliance,
discovery, push automation, distributed pollers, multi-tenancy/HA) are tracked
in [docs/roadmap.md](docs/roadmap.md).

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
