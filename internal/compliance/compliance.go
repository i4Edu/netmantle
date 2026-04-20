// Package compliance evaluates rule-based compliance against device
// configurations and probe outputs (Phases 4 + 8).
//
// Rule kinds:
//   - regex          : config must contain a match for Pattern
//   - must_include   : config must contain the literal Pattern
//   - must_exclude   : config must NOT contain the literal Pattern
//   - ordered_block  : Pattern is a JSON array of literal lines that must
//     appear in order somewhere in the config
package compliance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Rule mirrors a row in compliance_rules.
type Rule struct {
	ID          int64  `json:"id"`
	TenantID    int64  `json:"tenant_id"`
	GroupID     *int64 `json:"group_id,omitempty"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	Severity    string `json:"severity"`
	Description string `json:"description,omitempty"`
}

// Finding is the (device,rule) result.
type Finding struct {
	DeviceID int64  `json:"device_id"`
	RuleID   int64  `json:"rule_id"`
	Status   string `json:"status"` // "pass" | "fail"
	Detail   string `json:"detail,omitempty"`
}

// Service owns rule + ruleset CRUD and runs evaluations.
type Service struct {
	DB *sql.DB

	// OnTransition is called when a finding's status changes from a previously
	// stored value. Used by the API layer to dispatch notifications.
	OnTransition func(ctx context.Context, tenantID int64, f Finding, prev string)
}

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// CreateRule inserts a rule and validates the pattern.
func (s *Service) CreateRule(ctx context.Context, r Rule) (Rule, error) {
	if err := validateRule(r); err != nil {
		return Rule{}, err
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO compliance_rules(tenant_id, group_id, name, kind, pattern, severity, description, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TenantID, r.GroupID, r.Name, r.Kind, r.Pattern, defaultSeverity(r.Severity), r.Description,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return Rule{}, err
	}
	id, _ := res.LastInsertId()
	r.ID = id
	r.Severity = defaultSeverity(r.Severity)
	return r, nil
}

// UpsertRule inserts a rule or updates the kind/pattern/severity/description
// of an existing rule with the same (tenant_id, name). Callers use this when
// applying rule packs so re-applying a pack after a version bump is safe.
func (s *Service) UpsertRule(ctx context.Context, r Rule) (Rule, error) {
	if err := validateRule(r); err != nil {
		return Rule{}, err
	}
	r.Severity = defaultSeverity(r.Severity)
	var id int64
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, err
	}
	defer func() { _ = tx.Rollback() }()

	rowErr := selectRuleIDForScope(ctx, tx, r.TenantID, r.Name, r.GroupID, &id)
	switch {
	case rowErr == nil:
		_, err = tx.ExecContext(ctx, `
            UPDATE compliance_rules
            SET kind=?, pattern=?, severity=?, description=?
            WHERE id=?`,
			r.Kind, r.Pattern, r.Severity, r.Description, id)
		if err != nil {
			return Rule{}, err
		}
	case errors.Is(rowErr, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
            INSERT INTO compliance_rules(tenant_id, group_id, name, kind, pattern, severity, description, created_at)
            VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			r.TenantID, r.GroupID, r.Name, r.Kind, r.Pattern, r.Severity, r.Description, now)
		if err != nil {
			return Rule{}, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return Rule{}, err
		}
	default:
		return Rule{}, rowErr
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	r.ID = id
	return r, nil
}

func selectRuleIDForScope(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tenantID int64, name string, groupID *int64, id *int64) error {
	if groupID == nil {
		return q.QueryRowContext(ctx,
			`SELECT id FROM compliance_rules WHERE tenant_id=? AND name=? AND group_id IS NULL`,
			tenantID, name).Scan(id)
	}
	return q.QueryRowContext(ctx,
		`SELECT id FROM compliance_rules WHERE tenant_id=? AND name=? AND group_id=?`,
		tenantID, name, *groupID).Scan(id)
}

// ListRules returns all rules for a tenant.
func (s *Service) ListRules(ctx context.Context, tenantID int64) ([]Rule, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, group_id, name, kind, pattern, severity, IFNULL(description,'') FROM compliance_rules WHERE tenant_id=? ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.TenantID, &r.GroupID, &r.Name, &r.Kind, &r.Pattern, &r.Severity, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRule removes a rule and its findings.
func (s *Service) DeleteRule(ctx context.Context, tenantID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM compliance_rules WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}

// EvaluateText evaluates a single rule against a body of text.
func EvaluateText(r Rule, text string) Finding {
	switch r.Kind {
	case "regex":
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return Finding{RuleID: r.ID, Status: "fail", Detail: "invalid regex"}
		}
		if re.MatchString(text) {
			return Finding{RuleID: r.ID, Status: "pass"}
		}
		return Finding{RuleID: r.ID, Status: "fail", Detail: "pattern not found"}
	case "must_include":
		if strings.Contains(text, r.Pattern) {
			return Finding{RuleID: r.ID, Status: "pass"}
		}
		return Finding{RuleID: r.ID, Status: "fail", Detail: "missing required text"}
	case "must_exclude":
		if !strings.Contains(text, r.Pattern) {
			return Finding{RuleID: r.ID, Status: "pass"}
		}
		return Finding{RuleID: r.ID, Status: "fail", Detail: "forbidden text present"}
	case "ordered_block":
		var want []string
		if err := json.Unmarshal([]byte(r.Pattern), &want); err != nil {
			return Finding{RuleID: r.ID, Status: "fail", Detail: "ordered_block pattern must be JSON array"}
		}
		if orderedContains(text, want) {
			return Finding{RuleID: r.ID, Status: "pass"}
		}
		return Finding{RuleID: r.ID, Status: "fail", Detail: "ordered block not found"}
	}
	return Finding{RuleID: r.ID, Status: "fail", Detail: "unknown rule kind"}
}

// orderedContains reports whether the literal lines in `want` appear in
// `text` in order (not necessarily consecutively).
func orderedContains(text string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	idx := 0
	for _, line := range strings.Split(text, "\n") {
		if line == want[idx] {
			idx++
			if idx == len(want) {
				return true
			}
		}
	}
	return false
}

// EvaluateDevice runs every rule in the tenant against the supplied text,
// upserts findings, and invokes OnTransition for any status change.
// Returns the new findings.
func (s *Service) EvaluateDevice(ctx context.Context, tenantID, deviceID int64, text string) ([]Finding, error) {
	var deviceGroupID sql.NullInt64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT group_id FROM devices WHERE tenant_id=? AND id=?`,
		tenantID, deviceID).Scan(&deviceGroupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("compliance: device not found")
		}
		return nil, err
	}
	rules, err := s.listRulesForDevice(ctx, tenantID, deviceGroupID)
	if err != nil {
		return nil, err
	}
	out := make([]Finding, 0, len(rules))
	for _, r := range rules {
		f := EvaluateText(r, text)
		f.DeviceID = deviceID
		var prev string
		_ = s.DB.QueryRowContext(ctx,
			`SELECT status FROM compliance_findings WHERE device_id=? AND rule_id=?`,
			deviceID, r.ID).Scan(&prev)
		if _, err := s.DB.ExecContext(ctx, `
            INSERT INTO compliance_findings(tenant_id, device_id, rule_id, status, detail, created_at)
            VALUES(?, ?, ?, ?, ?, ?)
            ON CONFLICT(device_id, rule_id) DO UPDATE SET
                status = excluded.status, detail = excluded.detail, created_at = excluded.created_at`,
			tenantID, deviceID, r.ID, f.Status, f.Detail,
			time.Now().UTC().Format(time.RFC3339)); err != nil {
			return nil, err
		}
		if prev != "" && prev != f.Status && s.OnTransition != nil {
			s.OnTransition(ctx, tenantID, f, prev)
		}
		out = append(out, f)
	}
	if deviceGroupID.Valid {
		if _, err := s.DB.ExecContext(ctx, `
            DELETE FROM compliance_findings
            WHERE tenant_id=? AND device_id=? AND rule_id NOT IN (
                SELECT id FROM compliance_rules
                WHERE tenant_id=? AND (group_id IS NULL OR group_id=?)
            )`,
			tenantID, deviceID, tenantID, deviceGroupID.Int64); err != nil {
			return nil, err
		}
	} else {
		if _, err := s.DB.ExecContext(ctx, `
            DELETE FROM compliance_findings
            WHERE tenant_id=? AND device_id=? AND rule_id NOT IN (
                SELECT id FROM compliance_rules
                WHERE tenant_id=? AND group_id IS NULL
            )`,
			tenantID, deviceID, tenantID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListFindings returns findings for a tenant.
func (s *Service) ListFindings(ctx context.Context, tenantID int64) ([]Finding, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT device_id, rule_id, status, IFNULL(detail,'') FROM compliance_findings WHERE tenant_id=? ORDER BY device_id, rule_id`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.DeviceID, &f.RuleID, &f.Status, &f.Detail); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func validateRule(r Rule) error {
	if r.TenantID <= 0 || r.Name == "" || r.Pattern == "" {
		return errors.New("compliance: tenant_id, name, pattern required")
	}
	switch r.Kind {
	case "regex":
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return errors.New("compliance: invalid regex")
		}
	case "must_include", "must_exclude":
		// no further validation
	case "ordered_block":
		var v []string
		if err := json.Unmarshal([]byte(r.Pattern), &v); err != nil {
			return errors.New("compliance: ordered_block pattern must be JSON array of strings")
		}
	default:
		return errors.New("compliance: unknown kind")
	}
	return nil
}

func defaultSeverity(s string) string {
	switch s {
	case "low", "medium", "high", "critical":
		return s
	}
	return "medium"
}

func (s *Service) listRulesForDevice(ctx context.Context, tenantID int64, groupID sql.NullInt64) ([]Rule, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if groupID.Valid {
		rows, err = s.DB.QueryContext(ctx, `
            SELECT id, tenant_id, group_id, name, kind, pattern, severity, IFNULL(description,'')
            FROM compliance_rules
            WHERE tenant_id=? AND (group_id IS NULL OR group_id=?)
            ORDER BY name`,
			tenantID, groupID.Int64)
	} else {
		rows, err = s.DB.QueryContext(ctx, `
            SELECT id, tenant_id, group_id, name, kind, pattern, severity, IFNULL(description,'')
            FROM compliance_rules
            WHERE tenant_id=? AND group_id IS NULL
            ORDER BY name`,
			tenantID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.TenantID, &r.GroupID, &r.Name, &r.Kind, &r.Pattern, &r.Severity, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
