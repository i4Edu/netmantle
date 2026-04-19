# NetMantle user guide

This guide walks you through installing NetMantle, signing in for the first
time, registering devices and credentials, taking backups, and using the API
and operator endpoints. It targets the in-tree MVP that ships in this
repository (Phases 0–10); see [roadmap.md](roadmap.md) for what is still
scaffolded.

> **Conventions.** Commands prefixed with `$` are run on the host where you
> are deploying NetMantle. URLs assume the default `:8080` listener — adjust
> if you changed `server.address` in your config.

---

## 1. Install

NetMantle is a single static Go binary plus an embedded SQLite database and a
git-backed config store on disk. There are three supported install paths.

### 1.1 Build from source

Requirements: Go 1.25+ and `make`.

```bash
$ git clone https://github.com/i4Edu/netmantle.git
$ cd netmantle
$ make build          # produces ./bin/netmantle
$ make lint test      # optional but recommended
```

Then create a working data directory and start the server with the example
config:

```bash
$ mkdir -p data
$ NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-one' \
    ./bin/netmantle serve --config config.example.yaml
```

The server listens on `:8080` by default. If you do **not** set
`NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD`, a random password is generated and
printed to the log **once** on first start — capture it.

### 1.2 Docker

```bash
$ make docker
$ docker run --rm -p 8080:8080 \
    -v $(pwd)/data:/var/lib/netmantle \
    -e NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-one' \
    ghcr.io/i4edu/netmantle:dev
```

The image runs as a non-root user; mount a writable volume at
`/var/lib/netmantle` for the database and per-device git repos.

### 1.3 Kubernetes (Helm)

A starter chart lives at `deploy/helm/netmantle/`. Install with:

```bash
$ helm install netmantle deploy/helm/netmantle \
    --set image.tag=dev \
    --set bootstrapAdminPassword='choose-a-strong-one'
```

The chart provisions a `Deployment`, a `Service`, a `PersistentVolumeClaim`
for `/var/lib/netmantle`, and a `Secret` for the master passphrase and the
bootstrap admin password. See the chart `values.yaml` for tunables (resource
requests, ingress, leader-election, …).

---

## 2. First login

1. Open `http://<host>:8080/`.
2. Enter username `admin` and the bootstrap password from §1.
3. The header switches to show your username and exposes **API docs** and
   **Log out**.

![Sign-in screen](https://github.com/user-attachments/assets/c1ce27f8-93a1-49ab-8cf8-a344d3829b56)

After login the two-pane layout appears: device list and forms on the left,
device detail on the right.

![Empty dashboard after first login](https://github.com/user-attachments/assets/aeb253c3-1106-491e-b574-44e3fe6e8211)

> **Change the admin password.** The bootstrap password is meant to get you
> in. Once you are logged in, rotate it through the credentials/users API
> (or by setting a new `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD` and restarting —
> the new value is reconciled on boot).

---

## 3. Register a credential

Credentials are stored envelope-encrypted: NetMantle derives a key-encryption
key (KEK) from the configured `security.master_passphrase`, generates a fresh
data-encryption key per secret, and writes only the wrapped DEK plus
ciphertext to the database.

In the UI:

1. Expand **Add credential** in the left rail.
2. Provide a friendly **Name** (e.g. `lab-ssh`), the device **Username**, and
   the **Password** (or SSH key — paste the PEM body into the password
   field).
3. Click **Save**. The credential appears in the **Credential** dropdown of
   the **Add device** form.

Equivalent API call:

```bash
$ curl -s -b cookies.txt -X POST http://localhost:8080/api/v1/credentials \
    -H 'Content-Type: application/json' \
    -d '{"name":"lab-ssh","username":"netadmin","secret":"REDACTED"}'
```

(See §6 for how to obtain `cookies.txt` from `/api/v1/auth/login`.)

---

## 4. Register devices

1. Expand **Add device** in the left rail.
2. Fill in **Hostname**, **Address**, **Port** (defaults to `22`).
3. Pick a **Driver** — the dropdown is populated from the in-process driver
   registry and currently includes Cisco IOS / IOS-XR / NX-OS / NETCONF,
   Arista EOS, Junos CLI/NETCONF, MikroTik RouterOS, Nokia SR OS, gNMI,
   RESTCONF, generic SSH, and several ONT/OLT drivers.
4. Pick the **Credential** you created in §3.
5. Click **Create**. The device appears in the list, sorted alphabetically.

![Devices list with Add device form expanded](https://github.com/user-attachments/assets/17f115ae-a7c9-46cf-b090-c2e6fc464005)

---

## 5. Take a backup and inspect history

Click a device in the list. The right pane switches to its detail view:

- **Backup now** runs the configured driver against the device, stores the
  rendered configuration as a commit in the per-device git repo, records a
  `RunHistory` row, and updates "Latest configuration".
- **Delete** removes the device (the on-disk git repo is preserved so you can
  audit historical configs).
- **Latest configuration** shows the most recent successful capture.
- **Recent runs** lists the latest backup attempts with status, duration and
  any error message.

![Device detail pane](https://github.com/user-attachments/assets/05e87d3a-ace8-47c2-9553-27493b2a6c33)

Backups are also exposed by the API:

```bash
$ curl -s -b cookies.txt -X POST \
    http://localhost:8080/api/v1/devices/1/backup
$ curl -s -b cookies.txt \
    http://localhost:8080/api/v1/devices/1/runs | jq .
```

Diffs between any two stored versions are produced by the diff engine with
platform-aware ignore rules; ChangeEvents are recorded automatically and can
be routed to webhook / Slack / SMTP channels (see Phase 2 in the README
table and the change-rule API).

---

## 6. Using the API

NetMantle is API-first. Every UI action is backed by a documented endpoint.

- **OpenAPI spec:** `GET /api/openapi.yaml`
- **Interactive docs:** `GET /api/docs` (Swagger UI; loads its assets from a
  CDN, so it requires outbound internet from the browser)
- **Health:** `GET /healthz`, `GET /readyz`

Login and reuse the session cookie:

```bash
$ curl -s -c cookies.txt -X POST \
    http://localhost:8080/api/v1/auth/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"choose-a-strong-one"}'

$ curl -s -b cookies.txt http://localhost:8080/api/v1/devices | jq .
```

Common resources: `/api/v1/devices`, `/api/v1/credentials`,
`/api/v1/changes`, `/api/v1/search`, `/api/v1/compliance/*`,
`/api/v1/discovery/*`, `/api/v1/push-jobs`, `/api/v1/probes`,
`/api/v1/tenants`, `/api/v1/poller/*`, `/api/v1/terminal` (WebSocket).

---

## 7. Operator endpoints

### 7.1 Prometheus metrics

`GET /metrics` exposes Prometheus-format metrics with the `netmantle_`
prefix, including:

- `netmantle_uptime_seconds`
- `netmantle_http_requests_total{method,status}`
- `netmantle_http_request_duration_seconds_bucket{method,le}` (histogram)
- backup / run / probe outcome counters from the relevant subsystems

![Prometheus metrics endpoint](https://github.com/user-attachments/assets/51f690cf-2c98-4a54-8fc2-3d4366b55b16)

A Prometheus scrape config snippet:

```yaml
scrape_configs:
  - job_name: netmantle
    static_configs:
      - targets: ['netmantle:8080']
```

### 7.2 Health and readiness

- `GET /healthz` — process liveness; returns `{"status":"ok"}` whenever the
  HTTP server is up.
- `GET /readyz` — readiness; returns `200` only once the database has been
  migrated and the in-memory caches are warm. Use this for Kubernetes
  readiness probes.

### 7.3 Logging

Logs are emitted as structured JSON by default (`logging.format: json` in
`config.example.yaml`). Switch to `text` for human-friendly output during
development. The log line for HTTP requests includes `method`, `path`,
`status` and `dur_ms`.

---

## 8. Configuration reference

The full set of keys is documented inline in
[config.example.yaml](../config.example.yaml). Every key may also be set via
a `NETMANTLE_*` environment variable using uppercase, underscore-joined
names (e.g. `server.address` → `NETMANTLE_SERVER_ADDRESS`).

Production checklist:

- Set `security.master_passphrase` via
  `NETMANTLE_SECURITY_MASTER_PASSPHRASE` (rotate by re-wrapping; never edit
  the value in place without re-encrypting the credential store).
- Set `security.session_key` to a long random string so that sessions
  survive restarts.
- Run behind TLS termination (nginx, Caddy, an ingress controller, …).
- Mount `data/` on durable storage and back it up with the rest of your
  state — it contains the SQLite database **and** all per-device git repos.

---

## 9. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `401 Unauthorized` on every request | Session cookie missing or expired | Re-`POST /api/v1/auth/login` and reuse the new cookie |
| Bootstrap password not visible in logs | Server already initialised | Set `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD` and restart, or reset via the users API |
| Swagger UI page is blank | Browser blocked the unpkg CDN | Fetch `/api/openapi.yaml` directly and view it in any OpenAPI viewer |
| `Backup now` returns an error | Driver/credential mismatch or unreachable host | Check the device's `recent runs` for the error string; verify driver, credentials, network reachability |
| Metrics endpoint empty | Process just started | Trigger a few requests, then re-scrape |

For deeper digging, increase `logging.level` to `debug` and re-run the
operation; every subsystem (transport, drivers, scheduler, poller, gitops,
notify) logs at debug.

---

## 10. Where to next

- Browse [docs/architecture.md](architecture.md) for the high-level design.
- Read the ADRs under [docs/adr/](adr/) for the rationale behind the major
  technical decisions.
- See [docs/driver-sdk.md](driver-sdk.md) if you want to add a new device
  driver.
- Track ongoing work in [docs/roadmap.md](roadmap.md).
