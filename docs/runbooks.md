# Operational runbooks

This document is the on‑call reference for NetMantle operators. It covers
backup, restore, recovery, and rotation procedures for the artifacts that
NetMantle owns. For deployment shapes see [`deployment.md`](deployment.md);
for configuration details see [`configuration.md`](configuration.md); for the
threat model see [`threat-model.md`](threat-model.md).

> Each runbook follows the same shape: **Symptom → Diagnose → Mitigate →
> Recover → Postmortem**. Adjust paths to match your deployment (the examples
> use the in‑container defaults `/var/lib/netmantle/netmantle.db` and
> `/var/lib/netmantle/configs`).

---

## R0 — Health check & quick triage

NetMantle exposes:

- `GET /healthz` — process is alive
- `GET /readyz` — process is ready to serve (DB open, migrations applied)
- structured logs at `info` (or `debug`)

First three things to check on any incident:

1. Are both probes returning `200`? If `readyz` is failing, look at startup
   logs for migration / DB open errors.
2. Is the `scheduled-jobs` lease in the `scheduler_leases` table current? A
   stale lease past its TTL means no replica is currently leading scheduled
   work.
3. Is the persistent volume nearing capacity? SQLite + per‑device git repos
   grow over time.

---

## R1 — Bootstrap admin password lost

**Symptom.** Nobody has working admin credentials and the bootstrap log line
was not captured.

**Recover.**

1. Stop the NetMantle process / scale the Deployment to 0.
2. Open the SQLite database directly:
   ```
   sqlite3 /var/lib/netmantle/netmantle.db
   ```
3. Choose **one** recovery path below. Do **not** delete only the `admin`
   row and assume bootstrap will rerun: bootstrap only fires when the
   `users` table is **completely empty**
   (`SELECT COUNT(*) FROM users = 0`).

   **Option A — force bootstrap by emptying auth state**

   Clear dependent auth rows first, then remove all users:
   ```sql
   DELETE FROM sessions;
   DELETE FROM users;
   ```
   If your schema has additional tables with foreign keys to `users`, clear
   those as well; verify against `internal/storage/migrations/`.

   Restart NetMantle. Because `users` is now empty, the bootstrap path runs
   again and prints a fresh one‑time password. Capture it from the logs
   immediately, sign in, and recreate any other users you need.

   **Option B — reset the existing admin password in place**

   Generate a bcrypt hash for a temporary password on a trusted machine
   (any `bcrypt` cost ≥ 10), then update the admin row directly:
   ```sql
   UPDATE users
   SET password_hash = '$2b$12$REPLACE_WITH_BCRYPT_HASH'
   WHERE username = 'admin';
   ```
   This avoids relying on bootstrap logic and preserves the rest of the
   `users` table. Restart NetMantle, log in as `admin` with the temporary
   password, then rotate it immediately in the UI.

**Prevention.** Always preset `NETMANTLE_BOOTSTRAP_ADMIN_PASSWORD` from a
secret manager on the very first start, or pipe the start logs to a place
that retains them past the first restart.

---

## R2 — Master passphrase lost

**Symptom.** NetMantle starts, but every credential‑backed operation
(backups, GitOps mirror push) fails with envelope decryption errors.

**Recover.** There is no automated recovery — the KEK is derived from the
passphrase by design.

1. Restore the previous master passphrase from your secret manager / backup.
2. If the passphrase is irrecoverable: stored credentials and mirror tokens
   must be **re‑entered** by an operator. The configuration history in the
   git repos is unaffected (it is plaintext).

**Prevention.** Treat `NETMANTLE_SECURITY_MASTER_PASSPHRASE` like a database
master key. Store it in a managed secret store; back it up; document who can
read it.

---

## R3 — Master passphrase rotation

**Symptom.** Routine rotation, suspected compromise, or operator turnover.

**Procedure.**

1. Schedule a maintenance window — backups will be paused.
2. Snapshot the SQLite DB and the git repo root (see R6).
3. Stand up a parallel NetMantle pointing at a copy of the data with the
   **old** passphrase.
4. Use the API (or a one‑off Go program built against `internal/credentials`
   and `internal/crypto`) to list and re‑seal each credential and GitOps
   mirror token under the **new** passphrase. (A first‑class rotation CLI is
   tracked as a roadmap follow‑up — coordinate with engineering before
   automating this in production.)
5. Cut traffic to the new instance, retire the old.
6. Update the secret store and rotate access for old readers.

---

## R4 — Session key rotation

**Symptom.** Suspected `session_key` leak, or HA replicas were brought up
without a pinned `session_key` and are evicting each other's sessions.

**Procedure.**

1. Generate a new key (e.g. `openssl rand -hex 32`).
2. Update `NETMANTLE_SECURITY_SESSION_KEY` in your secret store.
3. Roll all replicas. **All active sessions are invalidated** — users will
   need to re‑authenticate.

---

## R5 — Device credential compromise

**Symptom.** A device account used by NetMantle is suspected leaked (e.g.
SSH key exposure, vendor disclosure).

**Procedure.**

1. Rotate the credential **on the device** first (out of band).
2. In NetMantle, update the credential record with the new secret. The new
   value is sealed under the current KEK on save.
3. Trigger a manual backup against an affected device to confirm the new
   credential works.
4. Review the audit log (`internal/audit`) for backup/terminal activity
   tied to the old credential during the suspected exposure window.

---

## R6 — Backup and restore of NetMantle's own state

NetMantle's persistent state is **two things**: the SQLite DB and the
per‑device git repository tree. Back them up together and atomically.

**Backup.** Quiesce briefly to get a consistent copy:

1. Take a snapshot via the SQLite online backup API:
   ```
   sqlite3 /var/lib/netmantle/netmantle.db ".backup '/backups/netmantle-$(date +%F).db'"
   ```
2. Tar the git repo root:
   ```
   tar -C /var/lib/netmantle -czf /backups/configs-$(date +%F).tgz configs
   ```
3. Ship both off‑host (object storage, off‑site).

In Kubernetes, the simplest pattern is a Velero (or equivalent) snapshot of
the PVC; combine with a pre‑hook that runs the SQLite `.backup` against a
sidecar mount.

**Restore.**

1. Stop NetMantle.
2. Restore the SQLite file to `database.dsn` and the configs directory to
   `storage.config_repo_root`. Preserve ownership (UID 65532 in container).
3. Ensure the **same** `master_passphrase` is in the environment.
4. Start NetMantle. Migrations run forward automatically; missing migrations
   apply, but the schema **never moves backward** — restoring an older DB
   into a newer binary is supported, the reverse is not.

---

## R7 — Storage near capacity

**Symptom.** Disk usage on the PVC / data volume is climbing.

**Diagnose.**

- `du -sh /var/lib/netmantle/{netmantle.db,configs}` — which side is growing?
- The configs tree grows with the number of devices × commit rate × config
  size.
- The DB grows with audit / change events / search index / probe results.

**Mitigate.**

- Resize the PVC (most CSI drivers support online resize).
- Lower probe / audit retention (the scheduler runs `probe-retention` hourly
  and prunes anything older than 30 days; tune this in
  `cmd/netmantle/main.go` if necessary).
- Run `VACUUM` on the SQLite DB during a maintenance window.

---

## R8 — GitOps mirror failures

**Symptom.** Logs show `gitops: mirror push` warnings after backups.

**Diagnose.**

- Is the configured remote reachable from the NetMantle pod / host?
- Has the mirror token expired or been revoked? (Tokens are stored in
  `gitops_mirrors.secret_envelope` and can be refreshed via the API.)
- Is the remote rejecting the push (force‑push restrictions, branch
  protection)?

**Mitigate.** Mirror failures are non‑fatal — the local git repos remain
authoritative. Once the remote is reachable / re‑authorised, the next backup
will resync.

---

## R9 — Scheduler leader stuck / no scheduled work running

**Symptom.** Scheduled jobs (e.g. `probe-retention`) stop firing; multiple
replicas show no leader.

**Diagnose.**

```sql
SELECT name, holder, expires_at FROM scheduler_leases;
```

- If `expires_at` is far in the past and no replica has reclaimed: there is
  likely DB write contention or clock skew between replicas.
- Confirm clocks are within a few seconds across replicas (NTP).

**Mitigate.** Restart all replicas — on startup they race for the lease and
the first to acquire it becomes leader. Verify scheduled work resumes.

---

## R10 — Migration failure on startup

**Symptom.** Process exits before serving HTTP with a `migrate` error in the
logs.

**Procedure.**

1. **Do not edit the migration file in place.** Migrations are append‑only
   and editing a released one corrupts the audit trail.
2. Restore the SQLite file from the most recent backup.
3. Roll back the binary to the previous version that matched that DB.
4. Open an issue with the failing migration name and the error message.

---

## Postmortem checklist

For any incident exercising one of these runbooks:

- [ ] Capture the timeline (first symptom, detection, mitigation, recovery).
- [ ] Identify whether the runbook needed a fix and update this file in the
      same PR as any code changes.
- [ ] If the incident touched a security boundary, also update
      [`SECURITY.md`](../SECURITY.md) and [`docs/threat-model.md`](threat-model.md).
- [ ] If a roadmap follow‑up would have prevented the incident (e.g. a
      first‑class rotation CLI, automated HA failover validation), file or
      reprioritise it in [`docs/roadmap.md`](roadmap.md).
