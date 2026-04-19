package changes

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/diff"
	"github.com/i4Edu/netmantle/internal/storage"
)

func TestRecordAndDiff(t *testing.T) {
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
	res, _ = db.Exec(`INSERT INTO devices(tenant_id, hostname, address, port, driver, created_at) VALUES(?, 'r1', '10.0.0.1', 22, 'cisco_ios', ?)`, tid, time.Now().Format(time.RFC3339))
	devID, _ := res.LastInsertId()

	store, _ := configstore.New(t.TempDir())
	c1, _ := store.Commit(tid, devID, "r1", "t", []configstore.Artifact{{Name: "running-config", Content: []byte("hostname r1\n")}})
	_, _ = db.Exec(`INSERT INTO config_versions(device_id, artifact, commit_sha, size_bytes, created_at) VALUES(?, ?, ?, ?, ?)`,
		devID, "running-config", c1.SHA, 12, time.Now().Format(time.RFC3339))
	c2, _ := store.Commit(tid, devID, "r1", "t", []configstore.Artifact{{Name: "running-config", Content: []byte("hostname r1\nlogging on\n")}})
	_, _ = db.Exec(`INSERT INTO config_versions(device_id, artifact, commit_sha, size_bytes, created_at) VALUES(?, ?, ?, ?, ?)`,
		devID, "running-config", c2.SHA, 24, time.Now().Format(time.RFC3339))

	svc := New(db, store, &diff.Engine{Rules: diff.DefaultRules()})
	ev, err := svc.Record(context.Background(), tid, devID, "running-config", c2.SHA)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if ev == nil || ev.Added != 1 || ev.Removed != 0 {
		t.Fatalf("event: %+v", ev)
	}

	list, err := svc.List(context.Background(), tid, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}

	d, err := svc.Diff(context.Background(), tid, ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d == "" {
		t.Fatal("empty diff")
	}

	if err := svc.MarkReviewed(context.Background(), tid, ev.ID); err != nil {
		t.Fatal(err)
	}
}
