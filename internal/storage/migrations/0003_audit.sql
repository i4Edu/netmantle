-- 0003 audit_log additions
--
-- Adds explicit actor (user) and source columns to the existing audit_log
-- table so every mutating handler can record who did what and via which
-- channel (ui, api, scheduler, poller, system). Existing rows are left
-- with NULL values; new writers populate them.

ALTER TABLE audit_log ADD COLUMN actor_user_id INTEGER;
ALTER TABLE audit_log ADD COLUMN source TEXT;

CREATE INDEX IF NOT EXISTS idx_audit_log_tenant ON audit_log(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor  ON audit_log(actor_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action, created_at DESC);
