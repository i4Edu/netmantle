package compliance

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func newSvc(t *testing.T) (*Service, int64, int64) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', ?)`, time.Now().Format(time.RFC3339))
	tid, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO devices(tenant_id, hostname, address, port, driver, created_at) VALUES(?, 'r1', '1', 22, 'cisco_ios', ?)`, tid, time.Now().Format(time.RFC3339))
	devID, _ := res.LastInsertId()
	return New(db), tid, devID
}

func TestRuleEvaluation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		pat    string
		text   string
		status string
	}{
		{"regex pass", "regex", `service password-encryption`, "service password-encryption\n", "pass"},
		{"regex fail", "regex", `enable secret`, "hostname r1\n", "fail"},
		{"include pass", "must_include", "logging on", "hostname r1\nlogging on\n", "pass"},
		{"include fail", "must_include", "logging on", "hostname r1\n", "fail"},
		{"exclude pass", "must_exclude", "snmp-server community public", "hostname r1\n", "pass"},
		{"exclude fail", "must_exclude", "snmp-server community public", "snmp-server community public RO\n", "fail"},
		{"ordered pass", "ordered_block", `["aaa new-model","aaa authentication login default group tacacs+"]`,
			"hostname r1\naaa new-model\nfoo\naaa authentication login default group tacacs+\n", "pass"},
		{"ordered fail", "ordered_block", `["a","b"]`, "b\na\n", "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := EvaluateText(Rule{Kind: tc.kind, Pattern: tc.pat}, tc.text)
			if f.Status != tc.status {
				t.Fatalf("status %s, want %s; detail=%s", f.Status, tc.status, f.Detail)
			}
		})
	}
}

func TestEvaluateDeviceWithTransitions(t *testing.T) {
	svc, tid, dev := newSvc(t)
	r, err := svc.CreateRule(context.Background(), Rule{TenantID: tid, Name: "needs-logging", Kind: "must_include", Pattern: "logging on"})
	if err != nil {
		t.Fatal(err)
	}
	transitions := 0
	svc.OnTransition = func(ctx context.Context, _ int64, _ Finding, _ string) { transitions++ }

	// First eval: no prior status → no transition.
	if _, err := svc.EvaluateDevice(context.Background(), tid, dev, "hostname r1\n"); err != nil {
		t.Fatal(err)
	}
	if transitions != 0 {
		t.Fatalf("transitions=%d after initial eval", transitions)
	}
	// Eval flipping to pass triggers transition.
	if _, err := svc.EvaluateDevice(context.Background(), tid, dev, "logging on\n"); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("transitions=%d after flip", transitions)
	}
	// Same result again: no new transition.
	if _, err := svc.EvaluateDevice(context.Background(), tid, dev, "logging on\n"); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 {
		t.Fatalf("unexpected re-transition: %d", transitions)
	}

	findings, err := svc.ListFindings(context.Background(), tid)
	if err != nil || len(findings) != 1 || findings[0].RuleID != r.ID {
		t.Fatalf("findings: %v %+v", err, findings)
	}
}

func TestRejectInvalidRule(t *testing.T) {
	svc, tid, _ := newSvc(t)
	if _, err := svc.CreateRule(context.Background(), Rule{TenantID: tid, Name: "bad", Kind: "regex", Pattern: "[invalid"}); err == nil {
		t.Fatal("expected regex validation error")
	}
	if _, err := svc.CreateRule(context.Background(), Rule{TenantID: tid, Name: "bad", Kind: "ordered_block", Pattern: "not json"}); err == nil {
		t.Fatal("expected ordered_block validation error")
	}
}

func TestEvaluateDeviceHonorsGroupScopedRules(t *testing.T) {
	svc, tid, dev := newSvc(t)
	// Create one group and move the device there.
	res, err := svc.DB.Exec(`INSERT INTO device_groups(tenant_id, name, created_at) VALUES(?, 'edge', ?)`,
		tid, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := res.LastInsertId()
	if _, err := svc.DB.Exec(`UPDATE devices SET group_id=? WHERE id=?`, gid, dev); err != nil {
		t.Fatal(err)
	}
	// Group-scoped rule.
	if _, err := svc.UpsertRule(context.Background(), Rule{
		TenantID: tid, GroupID: &gid,
		Name: "group-only", Kind: "must_include", Pattern: "set system services ssh",
	}); err != nil {
		t.Fatal(err)
	}
	// Global rule.
	if _, err := svc.UpsertRule(context.Background(), Rule{
		TenantID: tid,
		Name:     "global-only", Kind: "must_include", Pattern: "ntp server",
	}); err != nil {
		t.Fatal(err)
	}

	findings, err := svc.EvaluateDevice(context.Background(), tid, dev, "set system services ssh\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
}
