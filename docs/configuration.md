# Configuration reference

NetMantle reads YAML from the path given to `--config` and overlays a fixed
set of `NETMANTLE_*` environment variables on top. Env vars always win over
file values. The annotated example used by `make run` and the Docker image is
[`config.example.yaml`](../config.example.yaml).

This document is the complete reference. The authoritative source for the env
overlay is `internal/config/config.go` (`applyEnv`).

## Loading order

1. Built‑in defaults are applied (see `Default()` in `internal/config`).
2. If `--config FILE` is given, the YAML file is parsed and merged in.
3. The `NETMANTLE_*` environment variables listed below are applied last.
4. `Validate()` runs. Currently it requires `security.master_passphrase` to be
   set (the credential‑envelope KEK).

If validation fails the process exits with a non‑zero status and an error
message naming the missing field.

## File schema

```yaml
server:
  address: ":8080"        # listen address
  read_timeout: "30s"     # http.Server.ReadTimeout
  write_timeout: "30s"    # http.Server.WriteTimeout

database:
  driver: "sqlite"        # sqlite (default) — postgres is on the roadmap
  dsn:    "data/netmantle.db"

storage:
  config_repo_root: "data/configs"  # parent dir for per-device git repos

security:
  master_passphrase: ""             # REQUIRED — KEK derivation passphrase
  session_cookie:    "netmantle_session"
  session_key:       ""             # auto-generated when empty
  session_ttl:       "24h"

logging:
  level:  "info"          # debug | info | warn | error
  format: "json"          # text | json

backup:
  timeout: "60s"          # per-device backup timeout
  workers: 4              # max concurrent backup workers

poller:
  grpc:
    address: ""                # empty disables gRPC listener shell
    tls_cert_file: ""          # required when address is set
    tls_key_file: ""           # required when address is set
    tls_client_ca_file: ""     # required when address is set (mTLS)
```

## Environment variables

Only the variables in the table below are honoured. Anything else with the
`NETMANTLE_` prefix is ignored.

| Env var | YAML field | Notes |
|---------|------------|-------|
| `NETMANTLE_SERVER_ADDRESS` | `server.address` | e.g. `:8080`, `127.0.0.1:8080` |
| `NETMANTLE_DATABASE_DRIVER` | `database.driver` | `sqlite` today |
| `NETMANTLE_DATABASE_DSN` | `database.dsn` | path for sqlite |
| `NETMANTLE_STORAGE_CONFIG_REPO_ROOT` | `storage.config_repo_root` | parent dir for per‑device git repos |
| `NETMANTLE_SECURITY_MASTER_PASSPHRASE` | `security.master_passphrase` | **required**; never log; source from a secret manager in production |
| `NETMANTLE_SECURITY_SESSION_KEY` | `security.session_key` | hex/random string used to sign session cookies; auto‑generated if empty |
| `NETMANTLE_LOGGING_LEVEL` | `logging.level` | `debug` / `info` / `warn` / `error` |
| `NETMANTLE_LOGGING_FORMAT` | `logging.format` | `text` / `json` |
| `NETMANTLE_BACKUP_WORKERS` | `backup.workers` | integer |
| `NETMANTLE_BACKUP_TIMEOUT` | `backup.timeout` | Go duration, e.g. `60s`, `2m` |
| `NETMANTLE_POLLER_GRPC_ADDRESS` | `poller.grpc.address` | e.g. `:9443`; enables poller gRPC listener shell |
| `NETMANTLE_POLLER_GRPC_TLS_CERT_FILE` | `poller.grpc.tls_cert_file` | server certificate path for poller gRPC mTLS |
| `NETMANTLE_POLLER_GRPC_TLS_KEY_FILE` | `poller.grpc.tls_key_file` | server private key path for poller gRPC mTLS |
| `NETMANTLE_POLLER_GRPC_TLS_CLIENT_CA_FILE` | `poller.grpc.tls_client_ca_file` | client CA bundle for mTLS verification |

There is also one *startup‑only* env var consumed by `cmd/netmantle/main.go`:

| Env var | Purpose |
|---------|---------|
| `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD` | If set on the very first start (empty users table), the bootstrap admin is created with this password instead of a random one. The password is **not** echoed back. If unset, a random one‑time password is generated and printed at `WARN` level. |

## Field reference

### `server`

- **`address`** — what the HTTP server binds to. In production put TLS in
  front (reverse proxy or the Helm chart's `ingress`).
- **`read_timeout` / `write_timeout`** — HTTP server timeouts; the
  per‑request `ReadHeaderTimeout` is fixed at 10 s and `IdleTimeout` at 60 s.

### `database`

- **`driver`** — `sqlite` is the only supported driver today. PostgreSQL is
  tracked in [`docs/roadmap.md`](roadmap.md). Migrations run automatically on
  startup with a 30 s timeout (see `storage.Migrate`).
- **`dsn`** — for SQLite this is a filesystem path. Relative paths resolve
  against the working directory. In containers point this at the persistent
  volume (the published image defaults it to
  `/var/lib/netmantle/netmantle.db`).

### `storage`

- **`config_repo_root`** — directory where NetMantle creates one bare‑ish git
  repository per device under `configstore/`. This must live on persistent
  storage; losing it means losing configuration history. Size grows with the
  number of devices and the rate of change.

### `security`

- **`master_passphrase`** — derives the credential KEK used by
  `internal/crypto`. **Required.** Treat it like a database master key: store
  it in a secret manager, rotate by re‑sealing credentials. The Helm chart
  sources this from a Kubernetes `Secret`.
- **`session_cookie`** — name of the cookie used by the auth layer.
- **`session_key`** — HMAC key for signing session cookies. If empty, a fresh
  random key is generated at startup; this means restarts invalidate
  sessions, which is fine for single‑node deployments and safe (but logs
  users out) for multi‑replica deployments. Pin it explicitly in HA setups.
- **`session_ttl`** — cookie lifetime; default 24 h.

### `logging`

- **`level`** — `debug` enables verbose backup/transport tracing. Production
  should use `info` or higher.
- **`format`** — `json` for log aggregators, `text` for local development.

### `backup`

- **`timeout`** — per‑device timeout; also passed to the SSH dial.
- **`workers`** — concurrency cap for backup runs. Increase carefully:
  every worker holds an SSH session and a transient git working tree.

### `poller.grpc`

- **`address`** — optional bind address for the distributed-poller gRPC
  listener shell. Leave empty to disable the listener entirely.
- **`tls_cert_file` / `tls_key_file` / `tls_client_ca_file`** — mandatory
  when `poller.grpc.address` is set. NetMantle enforces mutual TLS
  (`RequireAndVerifyClientCert`) so only pollers with certificates signed by
  the configured client CA can complete the handshake.

## Secret handling

- `security.master_passphrase` and `security.session_key` are **secrets**.
  Never commit non‑placeholder values to a YAML file checked into git. Use
  env vars sourced from a secret manager (Kubernetes `Secret`, Docker
  secrets, Vault, SOPS‑decrypted file, …).
- Device credentials and GitOps mirror tokens are envelope‑encrypted with the
  KEK derived from `master_passphrase` and stored in the `credentials` and
  `gitops_mirrors` tables.
- See [`docs/threat-model.md`](threat-model.md) for the full surface and
  mitigation matrix and [`docs/runbooks.md`](runbooks.md) for rotation
  procedures.

## Sample production overlay

```yaml
server:
  address: ":8080"
database:
  driver: sqlite
  dsn: /var/lib/netmantle/netmantle.db
storage:
  config_repo_root: /var/lib/netmantle/configs
security:
  master_passphrase: ""   # set via NETMANTLE_SECURITY_MASTER_PASSPHRASE
  session_key: ""         # set via NETMANTLE_SECURITY_SESSION_KEY in HA
  session_ttl: "8h"
logging:
  level: info
  format: json
backup:
  timeout: "90s"
  workers: 8
```
