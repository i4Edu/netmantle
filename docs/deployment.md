# Deployment guide

NetMantle ships as a single Go binary plus three deployment shapes:

1. **Local binary** (development, small single‑host installs)
2. **Docker / Docker Compose** (single‑host production, evaluation)
3. **Helm chart on Kubernetes** (multi‑replica, HA‑capable scheduler)

For configuration details (every YAML field and `NETMANTLE_*` env var) see
[`docs/configuration.md`](configuration.md). For day‑two operations see
[`docs/runbooks.md`](runbooks.md). For the security posture and threat model
see [`SECURITY.md`](../SECURITY.md) and [`docs/threat-model.md`](threat-model.md).

---

## Persistent state

Regardless of shape, NetMantle owns three pieces of persistent state that
must survive restarts:

| Path / object | Purpose | Loss impact |
|---------------|---------|-------------|
| `database.dsn` (SQLite file) | Inventory, users, RBAC, audit, change events, search index, leases, compliance findings, poller registry, etc. | Loss = total platform reset |
| `storage.config_repo_root` (per‑device git repos) | Configuration history (every backup is a git commit) | Loss = entire history of device configs |
| `security.master_passphrase` (KEK material) | Decrypts envelope‑encrypted credentials and GitOps mirror tokens | Loss = stored credentials become unreadable; rotate via re‑seal |

The published image defaults the first two to `/var/lib/netmantle/` so a
single persistent volume is sufficient.

---

## 1. Local binary

Requires Go 1.25+ and `make`.

```bash
make build
./bin/netmantle serve --config /path/to/config.yaml
```

On first start with an empty database, NetMantle creates an admin user and
prints a one‑time bootstrap password at `WARN` level. Either capture it or
preset it via `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD` before the first start.

Useful CLI:

```
netmantle serve   [--config FILE]
netmantle version
netmantle help
```

The HTTP listen address is governed by `server.address` (default `:8080`).
Health and readiness endpoints are `/healthz` and `/readyz` (used by the
Helm chart's probes).

---

## 2. Docker / Docker Compose

### Image

The [`Dockerfile`](../Dockerfile) is a two‑stage build:

- Stage 1: `golang:1.25-alpine` — `go build` with `CGO_ENABLED=0` and a
  trimmed path; injects the version via `-ldflags`.
- Stage 2: `gcr.io/distroless/static:nonroot` — runs as UID 65532, no shell.

The image installs `config.example.yaml` to `/etc/netmantle/config.yaml` and
creates `/var/lib/netmantle/` owned by the non‑root user. It defaults the
data paths via env:

```
NETMANTLE_DATABASE_DSN=/var/lib/netmantle/netmantle.db
NETMANTLE_STORAGE_CONFIG_REPO_ROOT=/var/lib/netmantle/configs
```

Build it:

```bash
make docker
# or
docker build -t netmantle:local .
```

### Compose

[`docker-compose.yml`](../docker-compose.yml) is an evaluation/dev recipe. It
mounts a named volume `netmantle-data` at `/var/lib/netmantle` and presets a
development passphrase plus `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD=admin-please-change`.

```bash
docker compose up --build
# UI: http://localhost:8080
```

**Before exposing this anywhere non‑local:**

1. Replace `NETMANTLE_SECURITY_MASTER_PASSPHRASE` with a value sourced from a
   secret manager (Docker secret, env‑file, etc.). Do **not** commit the real
   value.
2. Remove `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD` and let NetMantle generate a
   one‑time password instead. Capture it from the logs and rotate the admin
   account immediately.
3. Front the container with TLS (reverse proxy: Caddy, nginx, Traefik, …).
4. Back up the `netmantle-data` volume on a schedule. See
   [`docs/runbooks.md`](runbooks.md) for the procedure.

---

## 3. Helm on Kubernetes

The chart lives at [`deploy/helm/netmantle`](../deploy/helm/netmantle) and
defaults to **2 replicas**. Replicas race for the leader lease
(`scheduler-leases` row, 30 s TTL); only the leader runs scheduled jobs.

### Install

```bash
helm install netmantle ./deploy/helm/netmantle \
  --namespace netmantle --create-namespace \
  --set masterPassphrase="$(openssl rand -hex 32)"
```

Key chart values (see [`values.yaml`](../deploy/helm/netmantle/values.yaml)):

| Key | Default | Notes |
|-----|---------|-------|
| `replicaCount` | `2` | Replicas race for leader lease; non‑leaders still serve API/UI |
| `image.repository` | `ghcr.io/i4edu/netmantle` | Override for private registries |
| `image.tag` | `""` | Defaults to `.Chart.AppVersion` |
| `service.type` | `ClusterIP` | Set `LoadBalancer` or use ingress for external access |
| `ingress.enabled` | `false` | Standard chart ingress block when enabled |
| `masterPassphrase` | `"change-me"` | **Set via `--set` from a secret manager**; rendered into a Kubernetes `Secret` and exposed as `NETMANTLE_SECURITY_MASTER_PASSPHRASE` |
| `database.driver` / `database.dsn` | `sqlite` / `/var/lib/netmantle/netmantle.db` | SQLite only today |
| `persistence.enabled` | `true` | PVC mounted at `/var/lib/netmantle` |
| `persistence.size` | `5Gi` | Size for SQLite DB + per‑device git repos |
| `podSecurityContext` | runAsNonRoot, UID 65532, fsGroup 65532 | |
| `securityContext` | readOnlyRootFilesystem, drop ALL caps | |
| `resources` | 100m/128Mi req, 500m/512Mi lim | Tune for fleet size |

Probes: liveness `/healthz`, readiness `/readyz`, both on the HTTP port.

### Sourcing the master passphrase from an external secret

Replace the `--set masterPassphrase=...` flow with a pre‑existing secret if
you manage secrets externally (External Secrets, Vault Agent, SOPS):

1. Create a `Secret` with key `master-passphrase` (the chart's deployment
   reads `<release>-secrets` / `master-passphrase`).
2. Either let the chart create a sibling `Secret` and patch it, or fork the
   chart's `templates/_helpers.tpl` to point at your existing secret.

(If you need this to be first‑class without forking, file an issue or PR —
see [`docs/roadmap.md`](roadmap.md).)

### Persistence sizing

SQLite + per‑device git repositories grow with:

- number of devices,
- average config size,
- frequency of change (each backup with diff is a git commit),
- retention of probe / audit data.

Start at `5Gi`. Monitor disk growth and resize the PVC ahead of saturation.

### High availability notes

- **Stateless replicas** for HTTP, **single leader** for scheduled work
  (probe pruning today; backup scheduling and saved‑search alerts as
  follow‑ups land).
- The leader lease lives in SQLite (`scheduler_leases` table). All replicas
  must mount the **same** PVC (`ReadWriteMany`) for the lease to work today.
  If your storage class is `RWO`, run `replicaCount: 1` until PostgreSQL
  support lands (tracked in [`docs/roadmap.md`](roadmap.md)).
- Automated HA failover validation is a roadmap follow‑up — exercise failover
  in a staging environment before relying on it.

---

## Upgrades

1. Read the release notes for breaking schema or API changes.
2. **Back up the SQLite file and the git repo root** before upgrading.
3. Pull the new image / chart.
4. Migrations run automatically at startup. Watch the logs for the
   `migrate` lines; failure exits non‑zero before the HTTP server starts.
5. Verify `/healthz`, `/readyz`, then run a manual backup against a known
   device.

---

## Observability

- **Metrics**: Prometheus metrics are exposed by `internal/observability`.
  Scrape the HTTP port; the path is wired by the API server.
- **Logs**: structured JSON when `logging.format: json`. Ship to your log
  aggregator; do not parse `WARN`‑level "bootstrap admin" lines into alerts —
  that is operator UX, not an error.
- **Audit**: see the `audit` table and `internal/audit` for the writer; the
  API exposes an audit query endpoint (see `internal/api/openapi/openapi.yaml`).

---

## Reverse proxy / TLS

NetMantle does not terminate TLS itself. Either:

- terminate at an ingress controller / cloud load balancer in Kubernetes, or
- terminate at a reverse proxy (Caddy, nginx, Traefik) in Docker / bare‑metal.

When proxying, preserve the `Host` header and forward `X-Forwarded-*`
headers; the embedded UI uses relative URLs and is base‑path agnostic for
the standard `/` deployment.
