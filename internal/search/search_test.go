package search

import (
	"context"
	"testing"

	"github.com/i4Edu/netmantle/internal/storage"
)

func TestIndexAndQuery(t *testing.T) {
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	tid := int64(1)
	_, _ = db.Exec(`INSERT INTO tenants(id, name, created_at) VALUES(?, 't', '2026-01-01T00:00:00Z')`, tid)
	_, _ = db.Exec(`INSERT INTO devices(id, tenant_id, hostname, address, port, driver, created_at) VALUES(7, ?, 'r1', '10.0.0.1', 22, 'cisco_ios', '2026-01-01T00:00:00Z')`, tid)

	s := New(db)
	if err := s.Index(context.Background(), tid, 7, "running-config", "deadbeef", []byte("hostname r1\nip route 0.0.0.0/0 10.0.0.254\n")); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Query(context.Background(), tid, "ip route", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) != 1 || hits[0].DeviceID != 7 {
		t.Fatalf("hits: %+v", hits)
	}
	if hits[0].Hostname != "r1" {
		t.Errorf("hostname: %q", hits[0].Hostname)
	}

	// Saved searches.
	id, err := s.SaveSearch(context.Background(), tid, 0, "default-route", "ip route", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("no id")
	}
	saved, _ := s.ListSaved(context.Background(), tid)
	if len(saved) != 1 || saved[0].Query != "ip route" {
		t.Fatalf("saved: %+v", saved)
	}

	// Re-index same SHA is idempotent.
	if err := s.Index(context.Background(), tid, 7, "running-config", "deadbeef", []byte("hostname r1\n")); err != nil {
		t.Fatal(err)
	}
	hits, _ = s.Query(context.Background(), tid, "ip route", 10)
	if len(hits) != 0 {
		t.Fatalf("expected re-index to replace, got %+v", hits)
	}
}
