-- 0002 phases 2..10 schema additions

-- Phase 2: change events + notifications
CREATE TABLE IF NOT EXISTS change_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id     INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    artifact      TEXT NOT NULL,
    old_sha       TEXT,
    new_sha       TEXT NOT NULL,
    added_lines   INTEGER NOT NULL DEFAULT 0,
    removed_lines INTEGER NOT NULL DEFAULT 0,
    reviewed      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_change_events_device ON change_events(device_id, created_at DESC);

CREATE TABLE IF NOT EXISTS notification_channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK(kind IN ('webhook','slack','email')),
    config     TEXT NOT NULL,                    -- JSON; secrets envelope-encrypted by caller
    created_at TEXT NOT NULL,
    UNIQUE(tenant_id, name)
);

CREATE TABLE IF NOT EXISTS notification_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    event_type TEXT NOT NULL,                    -- 'change' | 'compliance.transition' | 'runtime.violation'
    channel_id INTEGER NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
);

-- Phase 3: search + saved searches
CREATE VIRTUAL TABLE IF NOT EXISTS config_search USING fts5(
    artifact, body, device_id UNINDEXED, commit_sha UNINDEXED, tenant_id UNINDEXED
);

CREATE TABLE IF NOT EXISTS saved_searches (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    name       TEXT NOT NULL,
    query      TEXT NOT NULL,
    notify_channel_id INTEGER REFERENCES notification_channels(id) ON DELETE SET NULL,
    last_run_at TEXT,
    last_match_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

-- Phase 4: compliance
CREATE TABLE IF NOT EXISTS compliance_rules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id    INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK(kind IN ('regex','must_include','must_exclude','ordered_block')),
    pattern      TEXT NOT NULL,                  -- regex / literal / JSON-list-of-lines for ordered_block
    severity     TEXT NOT NULL DEFAULT 'medium', -- low|medium|high|critical
    description  TEXT,
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS compliance_rulesets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS compliance_ruleset_rules (
    ruleset_id INTEGER NOT NULL REFERENCES compliance_rulesets(id) ON DELETE CASCADE,
    rule_id    INTEGER NOT NULL REFERENCES compliance_rules(id) ON DELETE CASCADE,
    PRIMARY KEY(ruleset_id, rule_id)
);
CREATE TABLE IF NOT EXISTS compliance_ruleset_groups (
    ruleset_id INTEGER NOT NULL REFERENCES compliance_rulesets(id) ON DELETE CASCADE,
    group_id   INTEGER NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE,
    PRIMARY KEY(ruleset_id, group_id)
);
CREATE TABLE IF NOT EXISTS compliance_findings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL,
    device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    rule_id    INTEGER NOT NULL REFERENCES compliance_rules(id) ON DELETE CASCADE,
    status     TEXT NOT NULL CHECK(status IN ('pass','fail')),
    detail     TEXT,
    created_at TEXT NOT NULL,
    UNIQUE(device_id, rule_id)
);

-- Phase 5: discovery
CREATE TABLE IF NOT EXISTS discovery_scans (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id   INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    cidr        TEXT,
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    status      TEXT NOT NULL DEFAULT 'running'
);
CREATE TABLE IF NOT EXISTS discovery_results (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id     INTEGER NOT NULL REFERENCES discovery_scans(id) ON DELETE CASCADE,
    address     TEXT NOT NULL,
    fingerprint TEXT,
    suggested_driver TEXT,
    imported_device_id INTEGER REFERENCES devices(id) ON DELETE SET NULL,
    detail      TEXT
);

-- Phase 6: push automation
CREATE TABLE IF NOT EXISTS push_jobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    template   TEXT NOT NULL,
    variables  TEXT,                             -- JSON map<string,string>
    target_group_id INTEGER REFERENCES device_groups(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS push_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     INTEGER NOT NULL REFERENCES push_jobs(id) ON DELETE CASCADE,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status     TEXT NOT NULL DEFAULT 'running'
);
CREATE TABLE IF NOT EXISTS push_results (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id     INTEGER NOT NULL REFERENCES push_runs(id) ON DELETE CASCADE,
    device_id  INTEGER NOT NULL,
    rendered   TEXT NOT NULL,
    status     TEXT NOT NULL,
    output     TEXT,
    error      TEXT
);

-- Phase 7: terminal sessions (audit + recording)
CREATE TABLE IF NOT EXISTS terminal_sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL,
    user_id    INTEGER NOT NULL,
    device_id  INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    ended_at   TEXT,
    transcript TEXT
);
CREATE TABLE IF NOT EXISTS pollers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL,
    zone       TEXT NOT NULL,
    name       TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    last_seen  TEXT,
    created_at TEXT NOT NULL,
    UNIQUE(tenant_id, name)
);

-- Phase 8: probes + runtime checks
CREATE TABLE IF NOT EXISTS probes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL,
    name       TEXT NOT NULL UNIQUE,
    command    TEXT NOT NULL,
    interval_s INTEGER NOT NULL DEFAULT 300,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS probe_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    probe_id   INTEGER NOT NULL REFERENCES probes(id) ON DELETE CASCADE,
    device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    output     TEXT,
    error      TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_probe_runs_probe ON probe_runs(probe_id, created_at DESC);

-- Phase 9: tenant quotas + leader-elected scheduler
CREATE TABLE IF NOT EXISTS tenant_quotas (
    tenant_id   INTEGER PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    max_devices INTEGER NOT NULL DEFAULT 0   -- 0 = unlimited
);
CREATE TABLE IF NOT EXISTS scheduler_leases (
    name        TEXT PRIMARY KEY,
    holder      TEXT NOT NULL,
    expires_at  TEXT NOT NULL
);

-- Phase 10: GitOps mirror configuration
CREATE TABLE IF NOT EXISTS gitops_mirrors (
    tenant_id  INTEGER PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    remote_url TEXT NOT NULL,
    branch     TEXT NOT NULL DEFAULT 'main',
    secret_envelope TEXT,
    last_push_at TEXT
);
