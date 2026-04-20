-- 0006 compliance group-scoped rulepacks
--
-- Adds optional group scoping for compliance rules plus a table that stores
-- the selected built-in rule packs per device group.

ALTER TABLE compliance_rules ADD COLUMN group_id INTEGER REFERENCES device_groups(id) ON DELETE CASCADE;

-- Global rules: tenant/name must stay unique when group_id is NULL.
CREATE UNIQUE INDEX IF NOT EXISTS idx_compliance_rules_global_name
    ON compliance_rules(tenant_id, name)
    WHERE group_id IS NULL;

-- Group-scoped rules: tenant/group/name must be unique.
CREATE UNIQUE INDEX IF NOT EXISTS idx_compliance_rules_group_name
    ON compliance_rules(tenant_id, group_id, name)
    WHERE group_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_compliance_rules_scope
    ON compliance_rules(tenant_id, group_id, name);

CREATE TABLE IF NOT EXISTS compliance_rulepack_assignments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    group_id   INTEGER NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE,
    pack_name  TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    UNIQUE(tenant_id, group_id, pack_name)
);
CREATE INDEX IF NOT EXISTS idx_compliance_rulepack_assignments_tenant_group
    ON compliance_rulepack_assignments(tenant_id, group_id);
