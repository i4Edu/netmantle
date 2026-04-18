# NetMantle architecture (Phase 0 / 1)

NetMantle is a **modular monolith**: a single `netmantle` binary contains the
HTTP API, scheduler, workers, and embedded web UI. Modules are separated by
package boundaries inside `/internal` so that workers, pollers, or other
services can be extracted later without rewriting business logic.

```
                            +-------------------------+
                            |     Web UI (static)     |
                            +-----------+-------------+
                                        |
                                        v
+------------+        +-----------------+-----------------+
|  Operator  |  --->  |          netmantle (core)         |
+------------+        |                                   |
                      |  api/      auth/    devices/      |
                      |  drivers/  transport/             |
                      |  backup/   configstore/           |
                      |  storage/  observability/         |
                      +-----------------+-----------------+
                                        |
                +-----------------------+-----------------------+
                |                       |                       |
                v                       v                       v
        +---------------+      +----------------+      +-----------------+
        | SQLite/Postgres|     |  Git repos for  |     | Network devices |
        |  (metadata)    |     |  config history |     |  (SSH/Telnet/   |
        +---------------+      +----------------+      |   NETCONF/...)  |
                                                       +-----------------+
```

## Package responsibilities

| Package | Purpose |
| --- | --- |
| `cmd/netmantle` | Entry point + CLI (cobra-free; uses stdlib `flag`). |
| `internal/version` | Build version string injected via `-ldflags`. |
| `internal/config` | YAML + env config loading. |
| `internal/logging` | Structured logger (`log/slog`). |
| `internal/storage` | DB connection, migrations, repositories. |
| `internal/crypto` | Envelope encryption (AES-GCM, KEK from passphrase via scrypt). |
| `internal/auth` | Local users, password hashing, signed session cookies, RBAC. |
| `internal/api` | HTTP router, middlewares, handlers, OpenAPI spec. |
| `internal/devices` | Device / device-group domain logic. |
| `internal/credentials` | Encrypted credential CRUD. |
| `internal/drivers` | Driver interface + registry + builtin drivers. |
| `internal/transport` | SSH / Telnet (Phase 1: SSH). |
| `internal/configstore` | go-git wrapper; one repo per device. |
| `internal/backup` | Backup orchestration + worker pool. |
| `internal/observability` | Prometheus metrics, health endpoints. |
| `internal/web` | Embedded static UI assets. |

## Data flow: a backup

1. User clicks **Backup now** in the UI (or `POST /api/v1/devices/{id}/backup`).
2. `api` handler authorises the request, enqueues a job in `backup`.
3. A worker pulls credentials (decrypted just-in-time via `crypto`).
4. The driver opens an SSH session via `transport`, runs platform-specific
   commands, and returns one or more `ConfigArtifact`s.
5. `configstore` writes the artifact(s) to the device's git repo and commits.
6. A `config_versions` row is inserted, linked to the commit SHA, and a
   `backup_runs` row records the outcome. An audit-log entry is written.

## Future phases

See [roadmap.md](roadmap.md). The package boundaries above are intentionally
chosen so that, for example, a remote `cmd/poller` can later embed
`internal/drivers` + `internal/transport` and talk to the core over gRPC
without touching `internal/api`.
