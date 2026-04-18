package backup

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
	_ "github.com/i4Edu/netmantle/internal/drivers/builtin"
	"github.com/i4Edu/netmantle/internal/drivers/fakesession"
	"github.com/i4Edu/netmantle/internal/storage"
)

func TestBackupNowSuccess(t *testing.T) {
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', '2026-01-01T00:00:00Z')`)
	tid, _ := res.LastInsertId()

	devRepo := devices.NewRepo(db)
	sealer, _ := crypto.NewSealer("k")
	credRepo := credentials.NewRepo(db, sealer)
	store, _ := configstore.New(t.TempDir())

	cred, _ := credRepo.Create(context.Background(),
		credentials.Credential{TenantID: tid, Name: "c", Username: "u"}, "p")

	d, _ := devRepo.CreateDevice(context.Background(), devices.Device{
		TenantID: tid, Hostname: "r1", Address: "10.0.0.1", Port: 22,
		Driver: "cisco_ios", CredentialID: &cred.ID,
	})

	fakeSess := fakesession.New(map[string]string{
		"terminal length 0":   "",
		"show running-config": "hostname r1\n",
		"show startup-config": "hostname r1\n",
	})
	factory := func(ctx context.Context, dd devices.Device, user, pw string) (drivers.Session, func() error, error) {
		if user != "u" || pw != "p" {
			t.Errorf("creds not passed through: %q %q", user, pw)
		}
		return fakeSess, func() error { return nil }, nil
	}

	svc := New(devRepo, credRepo, store, db,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		5*time.Second, 2, factory)

	run, err := svc.BackupNow(context.Background(), tid, d.ID, "tester")
	if err != nil {
		t.Fatalf("BackupNow: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("status: %s", run.Status)
	}
	if run.CommitSHA == "" {
		t.Fatal("expected commit sha")
	}

	body, sha, err := svc.LatestVersion(context.Background(), tid, d.ID, "running-config")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if sha == "" || string(body) == "" {
		t.Fatalf("got %q sha=%s", body, sha)
	}

	runs, err := svc.ListRuns(context.Background(), d.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v %d", err, len(runs))
	}

	// Audit log written.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='device.backup'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("audit rows: %d", n)
	}
}
