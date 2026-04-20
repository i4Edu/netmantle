-- Automation v2: verify + rollback + schedules
ALTER TABLE push_jobs ADD COLUMN verify_command TEXT NOT NULL DEFAULT '';
ALTER TABLE push_jobs ADD COLUMN rollback_template TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS schedules (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id     INTEGER NOT NULL REFERENCES tenants(id),
    kind          TEXT NOT NULL CHECK(kind IN ('backup','push')),
    name          TEXT NOT NULL DEFAULT '',
    cron_expr     TEXT NOT NULL DEFAULT '',
    target_id     INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    last_run_at   TEXT NOT NULL DEFAULT '',
    next_run_at   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
