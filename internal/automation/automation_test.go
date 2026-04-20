package automation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/storage"
)

func setup(t *testing.T) (*Service, *devices.Repo, int64) {
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
	repo := devices.NewRepo(db)
	for _, n := range []string{"r1", "r2", "r3"} {
		_, _ = repo.CreateDevice(context.Background(), devices.Device{
			TenantID: tid, Hostname: n, Address: "1.2.3.4", Port: 22, Driver: "cisco_ios",
		})
	}
	exec := func(_ context.Context, d devices.Device, cfg string) (string, error) {
		if d.Hostname == "r3" {
			return "", errors.New("ssh refused")
		}
		return "applied:" + d.Hostname, nil
	}
	return New(db, repo, exec), repo, tid
}

func TestPreviewAndRun(t *testing.T) {
	svc, _, tid := setup(t)

	job, err := svc.CreateJob(context.Background(), Job{
		TenantID: tid, Name: "set-banner",
		Template:  "banner motd ^C welcome to {{.Device.Hostname}} {{.Vars.env}}^C",
		Variables: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatal(err)
	}

	prev, err := svc.Preview(context.Background(), tid, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prev) != 3 {
		t.Fatalf("preview count: %d", len(prev))
	}
	if !strings.Contains(prev[0].Rendered, "welcome to r1 prod") {
		t.Fatalf("render: %q", prev[0].Rendered)
	}

	results, err := svc.Run(context.Background(), tid, job.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var failed, applied int
	for _, r := range results {
		switch r.Status {
		case "applied":
			applied++
		case "failed":
			failed++
		}
	}
	if applied != 2 || failed != 1 {
		t.Fatalf("applied=%d failed=%d", applied, failed)
	}

	groups := GroupResults(results)
	// r1+r2 share status=applied but rendered text differs (hostname),
	// so they remain separate groups; r3 is its own failed group.
	if len(groups) != 3 {
		t.Fatalf("groups: %d", len(groups))
	}
}

func TestRejectInvalidTemplate(t *testing.T) {
	svc, _, tid := setup(t)
	if _, err := svc.CreateJob(context.Background(), Job{
		TenantID: tid, Name: "bad", Template: "{{ .Foo "}); err == nil {
		t.Fatal("expected template error")
	}
}

func TestRunPreFlightGuardBlocksPushAndAudits(t *testing.T) {
	svc, _, tid := setup(t)
	svc.Audit = audit.New(svc.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.PreFlight = func(_ context.Context, d devices.Device) error {
		if d.Hostname == "r2" {
			return errors.New("dial timeout")
		}
		return nil
	}

	job, err := svc.CreateJob(context.Background(), Job{
		TenantID: tid, Name: "preflight",
		Template:  "banner {{.Device.Hostname}}",
		Variables: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := svc.Run(context.Background(), tid, job.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var blocked bool
	for _, r := range results {
		if r.Hostname == "r2" {
			blocked = strings.Contains(r.Error, "pre-flight check failed")
		}
	}
	if !blocked {
		t.Fatal("expected r2 to be blocked by pre-flight check")
	}
	var n int
	if err := svc.DB.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='automation.apply.rollback_scaffold' AND target='device:2'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected high-priority rollback scaffold audit row for pre-flight failure")
	}
}

func TestRunApplyFailureWritesHighPriorityAudit(t *testing.T) {
	svc, _, tid := setup(t)
	svc.Audit = audit.New(svc.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))

	job, err := svc.CreateJob(context.Background(), Job{
		TenantID: tid, Name: "apply-fail",
		Template:  "banner {{.Device.Hostname}}",
		Variables: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(context.Background(), tid, job.ID, 2); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := svc.DB.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='automation.apply.rollback_scaffold' AND target='device:3'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected high-priority rollback scaffold audit row for apply failure")
	}
}
