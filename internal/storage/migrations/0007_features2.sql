-- 0007: scheduled backups, safe-mode push, TOTP MFA

-- TOTP MFA: optional per-user secret (base32-encoded); NULL = MFA disabled.
ALTER TABLE users ADD COLUMN totp_secret TEXT;

-- Safe-mode push jobs: capture pre-change config and auto-rollback if the
-- device becomes unreachable after a push.
ALTER TABLE push_jobs ADD COLUMN safe_mode         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE push_jobs ADD COLUMN rollback_timeout_s INTEGER NOT NULL DEFAULT 60;

-- MFA challenge tokens issued mid-login (valid 5 minutes). The client
-- must redeem the token with a valid TOTP code to receive a session cookie.
CREATE TABLE IF NOT EXISTS mfa_challenges (
    id         TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
