-- 0005 hardening: SSH known-hosts persistence + poller job queue
--
-- Migration notes
-- ---------------
-- ssh_known_hosts: new table; no existing data affected.
--   Rollback: migrations are append-only and cannot be reversed by editing
--   this file after it has been applied. To undo: restore from a pre-migration
--   backup, or ship a new follow-up migration/PR that drops this table.
--   No data transform is required — dropping the table is sufficient.
--   Every row carries tenant_id so cross-tenant isolation is preserved.
--
-- poller_jobs: new table; no existing data affected.
--   Rollback: same as ssh_known_hosts — restore from backup or ship a
--   follow-up migration/PR that drops this table. No data migration needed.
--   idempotency_key is unique across the whole table (not per-tenant) so
--   callers can use a UUID that embeds the tenant ID in the key string.

-- SSH known-hosts store (Phase 7 transport hardening — closes T7).
-- Each row pins one algorithm/key for a (tenant, host, port) tuple.
-- On first connection to a host the key is inserted (TOFU); subsequent
-- connections compare and reject if the key changed.
CREATE TABLE IF NOT EXISTS ssh_known_hosts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id   INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    host        TEXT    NOT NULL,
    port        INTEGER NOT NULL DEFAULT 22,
    algorithm   TEXT    NOT NULL,              -- e.g. "ssh-rsa", "ecdsa-sha2-nistp256"
    public_key  TEXT    NOT NULL,              -- base64-encoded raw public key bytes
    added_at    TEXT    NOT NULL,
    UNIQUE(tenant_id, host, port, algorithm)
);
CREATE INDEX IF NOT EXISTS idx_ssh_known_hosts_tenant ON ssh_known_hosts(tenant_id, host, port);

-- Poller job queue (Phase 7 follow-up — gRPC poller foundation).
-- A Job is a unit of work dispatched by the core to a remote poller.
-- idempotency_key lets callers safely retry without double-dispatch.
-- claimed_at records when a poller agent took the job; completed_at when
-- it finished (success or failure). expires_at allows the scheduler to
-- reclaim stale jobs whose poller disappeared.
CREATE TABLE IF NOT EXISTS poller_jobs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id        INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    poller_id        INTEGER REFERENCES pollers(id) ON DELETE SET NULL,
    idempotency_key  TEXT    NOT NULL UNIQUE,
    device_id        INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    job_type         TEXT    NOT NULL CHECK(job_type IN ('backup','probe','custom')),
    payload          TEXT,                     -- JSON job parameters
    status           TEXT    NOT NULL DEFAULT 'queued'
                     CHECK(status IN ('queued','claimed','running','done','failed','cancelled')),
    claimed_at       TEXT,
    completed_at     TEXT,
    result           TEXT,                     -- JSON result payload
    error            TEXT,
    created_at       TEXT    NOT NULL,
    expires_at       TEXT
);
CREATE INDEX IF NOT EXISTS idx_poller_jobs_tenant_status
    ON poller_jobs(tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_poller_jobs_poller
    ON poller_jobs(poller_id, status);
CREATE INDEX IF NOT EXISTS idx_poller_jobs_device
    ON poller_jobs(device_id, created_at DESC);
