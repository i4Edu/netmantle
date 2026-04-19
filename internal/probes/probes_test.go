package probes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func TestProbesLifecycle(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', ?)`, time.Now().Format(time.RFC3339))
	tid, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO devices(tenant_id, hostname, address, port, driver, created_at) VALUES(?, 'r1', '1', 22, 'cisco_ios', ?)`, tid, time.Now().Format(time.RFC3339))
	devID, _ := res.LastInsertId()

	svc := New(db)
	p, err := svc.Create(context.Background(), Probe{TenantID: tid, Name: "uptime", Command: "show version | inc uptime"})
	if err != nil {
		t.Fatal(err)
	}
	if p.IntervalS != 300 {
		t.Fatalf("default interval: %d", p.IntervalS)
	}

	if err := svc.RecordRun(context.Background(), p.ID, devID, "uptime is 1d", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRun(context.Background(), p.ID, devID, "", errors.New("ssh failed")); err != nil {
		t.Fatal(err)
	}
	runs, err := svc.LatestRuns(context.Background(), p.ID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %v %d", err, len(runs))
	}
	var sawError bool
	for _, r := range runs {
		if r.Error == "ssh failed" {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("error not stored: %+v", runs)
	}

	// Pruning.
	n, err := svc.PruneOlderThan(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned: %d", n)
	}
}

func TestRejectInvalid(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	_ = storage.Migrate(context.Background(), db)
	svc := New(db)
	if _, err := svc.Create(context.Background(), Probe{TenantID: 1, Name: "", Command: "x"}); err == nil {
		t.Fatal("expected error")
	}
}
