package changereq

import (
	"context"
	"errors"
	"testing"

	"github.com/i4Edu/netmantle/internal/storage"
)

func setup(t *testing.T) (*Service, int64, int64, int64, int64) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', '2026-01-01T00:00:00Z')`)
	tid, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO users(tenant_id, username, password_hash, role, created_at) VALUES(?, 'requester', 'x', 'operator', '2026-01-01T00:00:00Z')`, tid)
	requesterID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO users(tenant_id, username, password_hash, role, created_at) VALUES(?, 'reviewer', 'x', 'admin', '2026-01-01T00:00:00Z')`, tid)
	reviewerID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO push_jobs(tenant_id, name, template, variables, created_at) VALUES(?, 'job', 'hostname {{.Device.Hostname}}', '{}', '2026-01-01T00:00:00Z')`, tid)
	pushJobID, _ := res.LastInsertId()
	return New(db), tid, requesterID, reviewerID, pushJobID
}

func TestPushHappyPath(t *testing.T) {
	svc, tid, req, rev, jobID := setup(t)
	ctx := context.Background()
	pj := jobID
	cr, err := svc.Create(ctx, ChangeRequest{
		TenantID: tid, Kind: KindPush, Title: "deploy", RequesterID: req, PushJobID: &pj,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cr.Status != StatusDraft {
		t.Fatalf("want draft, got %s", cr.Status)
	}
	if cr, err = svc.Submit(ctx, tid, cr.ID, req); err != nil {
		t.Fatal(err)
	}
	if cr.Status != StatusSubmitted || cr.SubmittedAt == nil {
		t.Fatalf("submit: %+v", cr)
	}
	if cr, err = svc.Approve(ctx, tid, cr.ID, rev, "lgtm", false); err != nil {
		t.Fatal(err)
	}
	if cr.Status != StatusApproved || cr.ReviewerID == nil || *cr.ReviewerID != rev {
		t.Fatalf("approve: %+v", cr)
	}
	if !CanApply(cr) {
		t.Fatal("CanApply should be true after approve")
	}
	if cr, err = svc.MarkApplied(ctx, tid, cr.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	if cr.Status != StatusApplied || cr.Result != "ok" || cr.AppliedAt == nil {
		t.Fatalf("apply: %+v", cr)
	}
	// Terminal: no more transitions accepted.
	if _, err := svc.Cancel(ctx, tid, cr.ID, req, ""); !errors.Is(err, ErrTerminal) {
		t.Fatalf("expected ErrTerminal, got %v", err)
	}
	// Events recorded: created/draft, submitted, approved, applied → 4 rows.
	evs, err := svc.Events(ctx, cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 {
		t.Fatalf("want 4 events, got %d (%+v)", len(evs), evs)
	}
}

func TestRejectAndSelfApproval(t *testing.T) {
	svc, tid, req, rev, jobID := setup(t)
	ctx := context.Background()
	pj := jobID
	cr, _ := svc.Create(ctx, ChangeRequest{TenantID: tid, Kind: KindPush, Title: "x", RequesterID: req, PushJobID: &pj})
	cr, _ = svc.Submit(ctx, tid, cr.ID, req)

	if _, err := svc.Approve(ctx, tid, cr.ID, req, "self", false); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("expected ErrSelfApproval, got %v", err)
	}
	// allowSelf bypass works (admins).
	if _, err := svc.Approve(ctx, tid, cr.ID, req, "admin self-approve", true); err != nil {
		t.Fatalf("self-approve allowed: %v", err)
	}
	// Now reject from approved is invalid.
	if _, err := svc.Reject(ctx, tid, cr.ID, rev, "no"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition rejecting after approve, got %v", err)
	}
}

func TestInvalidTransitionsAndKinds(t *testing.T) {
	svc, tid, req, _, jobID := setup(t)
	ctx := context.Background()
	// Push without push_job_id.
	if _, err := svc.Create(ctx, ChangeRequest{TenantID: tid, Kind: KindPush, Title: "x", RequesterID: req}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	// Rollback without device/artifact/sha.
	if _, err := svc.Create(ctx, ChangeRequest{TenantID: tid, Kind: KindRollback, Title: "x", RequesterID: req}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	// Unknown kind.
	if _, err := svc.Create(ctx, ChangeRequest{TenantID: tid, Kind: "weird", Title: "x", RequesterID: req}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	// Cannot approve a draft (must submit first).
	pj := jobID
	cr, _ := svc.Create(ctx, ChangeRequest{TenantID: tid, Kind: KindPush, Title: "x", RequesterID: req, PushJobID: &pj})
	if _, err := svc.Approve(ctx, tid, cr.ID, req+999, "", false); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestRollbackKind(t *testing.T) {
	svc, tid, req, rev, _ := setup(t)
	ctx := context.Background()
	res, _ := svc.DB.Exec(`INSERT INTO devices(tenant_id, hostname, address, port, driver, created_at) VALUES(?, 'r1', '10.0.0.1', 22, 'cisco_ios', '2026-01-01T00:00:00Z')`, tid)
	devID, _ := res.LastInsertId()
	cr, err := svc.Create(ctx, ChangeRequest{
		TenantID: tid, Kind: KindRollback, Title: "rollback", RequesterID: req,
		DeviceID: &devID, Artifact: "running-config", TargetSHA: "deadbeef",
		Payload: "hostname old",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, _ = svc.Submit(ctx, tid, cr.ID, req)
	cr, err = svc.Approve(ctx, tid, cr.ID, rev, "ok", false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, tid, cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindRollback || got.TargetSHA != "deadbeef" || got.Payload != "hostname old" {
		t.Fatalf("rollback round-trip: %+v", got)
	}
	if got.Status != StatusApproved {
		t.Fatalf("status: %s", got.Status)
	}
}

func TestListFilters(t *testing.T) {
	svc, tid, req, rev, jobID := setup(t)
	ctx := context.Background()
	pj := jobID
	for i := 0; i < 3; i++ {
		cr, _ := svc.Create(ctx, ChangeRequest{TenantID: tid, Kind: KindPush, Title: "x", RequesterID: req, PushJobID: &pj})
		if i < 2 {
			cr, _ = svc.Submit(ctx, tid, cr.ID, req)
			if i == 0 {
				_, _ = svc.Approve(ctx, tid, cr.ID, rev, "", false)
			}
		}
	}
	all, err := svc.List(ctx, tid, "", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("list all: %v, %d", err, len(all))
	}
	subs, err := svc.List(ctx, tid, StatusSubmitted, 10)
	if err != nil || len(subs) != 1 {
		t.Fatalf("list submitted: %v, %d", err, len(subs))
	}
	apprs, err := svc.List(ctx, tid, StatusApproved, 10)
	if err != nil || len(apprs) != 1 {
		t.Fatalf("list approved: %v, %d", err, len(apprs))
	}
}
