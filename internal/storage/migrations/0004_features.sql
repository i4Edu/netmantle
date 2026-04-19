-- 0004 zero-credential / approval / accountability / rollback / api-tokens
--
-- Adds the schema needed for the five product capabilities introduced in
-- this PR. All tables are tenant-scoped from day one. The `audit_log`
-- table gains a `request_id` column so that per-request correlation IDs
-- (X-Request-ID) can be threaded through every audit row a single API
-- call produces. The existing `credentials` table gains a `last_used_at`
-- column so the new `credentials.Use(...)` helper can record the last
-- time the secret was decrypted for a transport call.

-- Phase A — credential use tracking.
ALTER TABLE credentials ADD COLUMN last_used_at TEXT;

-- Phase C — request-id correlation on audit rows.
ALTER TABLE audit_log ADD COLUMN request_id TEXT;
CREATE INDEX IF NOT EXISTS idx_audit_log_request ON audit_log(request_id);

-- Phase B / D — change-request queue.
--
-- A ChangeRequest is the unit Senior staff approve. `kind` distinguishes
-- a routine push (referencing a push_jobs row) from a rollback (carrying
-- the historical commit SHA we'll re-apply). `status` follows a strict
-- state machine enforced in code (internal/changereq).
CREATE TABLE IF NOT EXISTS change_requests (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id       INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK(kind IN ('push','rollback')),
    title           TEXT NOT NULL,
    description     TEXT,
    requester_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reviewer_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    status          TEXT NOT NULL CHECK(status IN ('draft','submitted','approved','rejected','applied','failed','cancelled')),
    decision_reason TEXT,
    push_job_id     INTEGER REFERENCES push_jobs(id) ON DELETE SET NULL,
    variables       TEXT,                 -- JSON snapshot of overrides for push
    device_id       INTEGER REFERENCES devices(id) ON DELETE SET NULL,
    artifact        TEXT,                 -- rollback target artifact name
    target_sha      TEXT,                 -- rollback target commit SHA
    payload         TEXT,                 -- rendered config (rollback) or null
    result          TEXT,                 -- post-apply executor output
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    submitted_at    TEXT,
    decided_at      TEXT,
    applied_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_change_requests_tenant_status
    ON change_requests(tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_change_requests_device
    ON change_requests(device_id, created_at DESC);

-- Audit trail of every state transition for a change request. The
-- audit_log already records cross-cutting events; this table is the
-- canonical timeline for a single request.
CREATE TABLE IF NOT EXISTS change_request_events (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    change_request_id INTEGER NOT NULL REFERENCES change_requests(id) ON DELETE CASCADE,
    actor_user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    from_status       TEXT,
    to_status         TEXT NOT NULL,
    note              TEXT,
    created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_change_request_events_cr
    ON change_request_events(change_request_id, created_at);

-- Phase E — API tokens for billing/provisioning integrations.
--
-- Tokens are presented as `nmt_<prefix>_<secret>`; the secret half is
-- bcrypted at rest. `prefix` is an indexable lookup key; a constant-time
-- bcrypt compare against `secret_hash` provides authentication. Scopes
-- are a comma-separated list (e.g. "device:read,changereq:approve").
CREATE TABLE IF NOT EXISTS api_tokens (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id     INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    owner_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    prefix        TEXT NOT NULL UNIQUE,
    secret_hash   TEXT NOT NULL,
    scopes        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    expires_at    TEXT,
    last_used_at  TEXT,
    revoked_at    TEXT,
    UNIQUE(tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_tenant ON api_tokens(tenant_id);
