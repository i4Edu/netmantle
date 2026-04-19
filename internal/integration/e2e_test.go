// Package integration contains end-to-end tests that exercise the full
// backup → diff → compliance workflow in a single in-memory environment.
// These tests act as confidence gates before a stable tag: they verify that
// the three core Phase 0–2 + Phase 4 services compose correctly.
package integration

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/changes"
	"github.com/i4Edu/netmantle/internal/compliance"
	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/diff"
	"github.com/i4Edu/netmantle/internal/drivers"
	_ "github.com/i4Edu/netmantle/internal/drivers/builtin"
	"github.com/i4Edu/netmantle/internal/drivers/fakesession"
	"github.com/i4Edu/netmantle/internal/storage"
)

// testEnv holds all wired-up services for an integration test.
type testEnv struct {
	backupSvc     *backup.Service
	changesSvc    *changes.Service
	complianceSvc *compliance.Service
	tenantID      int64
	devID         int64
	// configBody is updated between backup calls to simulate device config changes.
	configBody string
}

// newEnv creates a fully-wired testEnv backed by an in-memory SQLite database.
// It uses the generic_ssh driver so that each backup produces exactly one
// config artifact ("configuration"), which keeps change-event counts predictable.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('acme', '2026-01-01T00:00:00Z')`)
	tenantID, _ := res.LastInsertId()

	devRepo := devices.NewRepo(db)
	sealer, _ := crypto.NewSealer("integration-test-key")
	credRepo := credentials.NewRepo(db, sealer)
	store, _ := configstore.New(t.TempDir())

	cred, _ := credRepo.Create(context.Background(),
		credentials.Credential{TenantID: tenantID, Name: "c", Username: "u"}, "p")
	// generic_ssh tries "show configuration" first, then "show running-config".
	dev, _ := devRepo.CreateDevice(context.Background(), devices.Device{
		TenantID: tenantID, Hostname: "r1", Address: "10.0.0.1", Port: 22,
		Driver: "generic_ssh", CredentialID: &cred.ID,
	})

	env := &testEnv{
		tenantID:   tenantID,
		devID:      dev.ID,
		configBody: "hostname r1\n",
	}

	factory := func(ctx context.Context, d devices.Device, _, _ string) (drivers.Session, func() error, error) {
		sess := fakesession.New(map[string]string{
			// generic_ssh calls "show configuration" first; it falls back to
			// "show running-config" when that command is not available.
			"show running-config": env.configBody,
		})
		return sess, func() error { return nil }, nil
	}

	env.changesSvc = changes.New(db, store, &diff.Engine{Rules: diff.DefaultRules()})
	env.complianceSvc = compliance.New(db)

	env.backupSvc = backup.New(
		devRepo, credRepo, store, db,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		5*time.Second, 2, factory,
	)

	// Wire PostCommit hooks: changes recording + compliance evaluation.
	env.backupSvc.PostCommit = []backup.PostCommitHook{
		func(ctx context.Context, tID int64, d devices.Device, sha string, arts []configstore.Artifact) {
			for _, a := range arts {
				_, _ = env.changesSvc.Record(ctx, tID, d.ID, a.Name, sha)
			}
		},
		func(ctx context.Context, tID int64, d devices.Device, _ string, arts []configstore.Artifact) {
			for _, a := range arts {
				_, _ = env.complianceSvc.EvaluateDevice(ctx, tID, d.ID, string(a.Content))
			}
		},
	}

	return env
}

// mustBackup runs a backup and fails the test on error.
func mustBackup(t *testing.T, env *testEnv) *backup.Run {
	t.Helper()
	run, err := env.backupSvc.BackupNow(context.Background(), env.tenantID, env.devID, "tester")
	if err != nil {
		t.Fatalf("BackupNow: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("backup status: %s (error: %s)", run.Status, run.Error)
	}
	return run
}

// TestBackupDiffComplianceWorkflow is the primary end-to-end gate:
//
//  1. Back up a device (initial config) — generates 1 ChangeEvent (vs empty).
//  2. Back up again with a changed config — generates 1 more ChangeEvent.
//  3. The diff for the second event contains the expected added/removed lines.
//  4. A compliance rule evaluates to fail on the changed config and pass after the fix.
func TestBackupDiffComplianceWorkflow(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()

	// ---- Backup #1 (initial state) ----------------------------------------
	env.configBody = "hostname r1\nno ip http server\n"
	mustBackup(t, env)

	// The first backup generates a change event (new config vs empty baseline).
	evsBefore, _ := env.changesSvc.List(ctx, env.tenantID, 10)
	initialCount := len(evsBefore)
	if initialCount == 0 {
		t.Fatal("expected at least 1 change event after first backup")
	}

	// ---- Backup #2 (config changes) ----------------------------------------
	env.configBody = "hostname r1\nip http server\n"
	mustBackup(t, env)

	// Exactly one new ChangeEvent must have been appended.
	evsAfter, err := env.changesSvc.List(ctx, env.tenantID, 10)
	if err != nil {
		t.Fatalf("list changes: %v", err)
	}
	if len(evsAfter) != initialCount+1 {
		t.Fatalf("expected %d events after change, got %d", initialCount+1, len(evsAfter))
	}

	// Find the event that represents the diff between backup #1 and backup #2:
	// it is the one with a non-empty OldSHA (it has a predecessor). The initial
	// backup produces an event with an empty OldSHA (diff vs the empty baseline).
	var ev changes.Event
	for _, e := range evsAfter {
		if e.OldSHA != "" {
			ev = e
			break
		}
	}
	if ev.ID == 0 {
		t.Fatal("could not find a change event with a predecessor (OldSHA)")
	}
	if ev.Added == 0 && ev.Removed == 0 {
		t.Fatalf("change event has no added/removed lines: %+v", ev)
	}

	// ---- Diff retrieval ---------------------------------------------------
	diffText, err := env.changesSvc.Diff(ctx, env.tenantID, ev.ID)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diffText == "" {
		t.Fatal("expected non-empty diff")
	}
	// "no ip http server" was removed; "ip http server" was added.
	if !strings.Contains(diffText, "- no ip http server") {
		t.Errorf("diff missing removal: %q", diffText)
	}
	if !strings.Contains(diffText, "+ ip http server") {
		t.Errorf("diff missing addition: %q", diffText)
	}

	// ---- Compliance evaluation -------------------------------------------
	rule, err := env.complianceSvc.CreateRule(ctx, compliance.Rule{
		TenantID: env.tenantID, Name: "require-http-disabled",
		Kind: "must_include", Pattern: "no ip http server",
		Severity: "high",
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// Evaluate against the changed config: "ip http server" present, "no ip http server" absent.
	findings, err := env.complianceSvc.EvaluateDevice(ctx, env.tenantID, env.devID, env.configBody)
	if err != nil {
		t.Fatalf("evaluate device: %v", err)
	}
	var gotFail bool
	for _, f := range findings {
		if f.RuleID == rule.ID && f.Status == "fail" {
			gotFail = true
		}
	}
	if !gotFail {
		t.Fatalf("expected failing finding for rule %d, got: %+v", rule.ID, findings)
	}

	// Fix: revert to config with "no ip http server" → finding flips to pass.
	findings, err = env.complianceSvc.EvaluateDevice(ctx, env.tenantID, env.devID, "hostname r1\nno ip http server\n")
	if err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	for _, f := range findings {
		if f.RuleID == rule.ID && f.Status != "pass" {
			t.Errorf("expected pass after fix, got %s", f.Status)
		}
	}
}

// TestBackupIdempotence verifies that backing up the same config twice does
// not produce a new ChangeEvent (the diff engine marks it as identical).
func TestBackupIdempotence(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()

	env.configBody = "hostname r1\n"
	mustBackup(t, env)

	evsBefore, _ := env.changesSvc.List(ctx, env.tenantID, 10)
	countBefore := len(evsBefore)

	// Second backup with same config — should produce no new event.
	mustBackup(t, env)

	evsAfter, _ := env.changesSvc.List(ctx, env.tenantID, 10)
	if len(evsAfter) != countBefore {
		t.Fatalf("expected %d events (same as before), got %d", countBefore, len(evsAfter))
	}
}

// TestMultipleChangesAccumulate verifies that successive config changes each
// produce their own ChangeEvent, and that the list is ordered newest-first.
func TestMultipleChangesAccumulate(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()

	// Three distinct configs → three backups → three change events.
	configs := []string{
		"hostname r1\n",
		"hostname r1\nntp server 1.1.1.1\n",
		"hostname r1\nntp server 1.1.1.1\nip domain-name example.com\n",
	}
	for _, cfg := range configs {
		env.configBody = cfg
		mustBackup(t, env)
	}

	evs, err := env.changesSvc.List(ctx, env.tenantID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Each distinct config produces one change event (including the first vs empty).
	if len(evs) != 3 {
		t.Fatalf("expected 3 change events, got %d", len(evs))
	}
	// Newest first.
	if evs[0].CreatedAt.Before(evs[1].CreatedAt) {
		t.Error("expected descending order (newest first)")
	}
}

// TestComplianceTransitionNotification verifies that OnTransition is called
// exactly when a finding's status flips, and not on stable subsequent evals.
func TestComplianceTransitionNotification(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()

	svc := env.complianceSvc
	var transitions []string
	svc.OnTransition = func(_ context.Context, _ int64, f compliance.Finding, prev string) {
		transitions = append(transitions, prev+"->"+f.Status)
	}

	rule, err := svc.CreateRule(ctx, compliance.Rule{
		TenantID: env.tenantID, Name: "require-ntp", Kind: "must_include", Pattern: "ntp server",
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	// Verify the rule was stored with a valid ID; the transition assertions
	// below rely on the rule existing in the database.
	if rule.ID == 0 {
		t.Fatal("expected non-zero rule ID")
	}

	// First evaluation: no prior finding → no transition callback.
	_, _ = svc.EvaluateDevice(ctx, env.tenantID, env.devID, "hostname x\n")
	if len(transitions) != 0 {
		t.Fatalf("expected no transition on first eval, got %v", transitions)
	}

	// Second evaluation with passing status → transition from fail→pass.
	_, _ = svc.EvaluateDevice(ctx, env.tenantID, env.devID, "hostname x\nntp server 1.1.1.1\n")
	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition, got %v", transitions)
	}
	if !strings.Contains(transitions[0], "fail->pass") {
		t.Errorf("unexpected transition: %v", transitions[0])
	}

	// Third evaluation with same passing status → no additional transition.
	_, _ = svc.EvaluateDevice(ctx, env.tenantID, env.devID, "hostname x\nntp server 2.2.2.2\n")
	if len(transitions) != 1 {
		t.Fatalf("expected still 1 transition after stable state, got %v", transitions)
	}
}
