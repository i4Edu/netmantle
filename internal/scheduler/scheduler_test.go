package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func TestLeaseHandoff(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	a := NewLease(db, "backups", 5*time.Second)
	b := NewLease(db, "backups", 5*time.Second)

	ok, err := a.Acquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("a acquire: %v %v", err, ok)
	}
	// b cannot steal while a's lease is fresh.
	ok, _ = b.Acquire(context.Background())
	if ok {
		t.Fatal("b stole live lease")
	}
	// Manually expire a's lease.
	_, _ = db.Exec(`UPDATE scheduler_leases SET expires_at=? WHERE name='backups'`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	ok, _ = b.Acquire(context.Background())
	if !ok {
		t.Fatal("b should take over after expiry")
	}
	// a renewing should now fail (b holds).
	ok, _ = a.Acquire(context.Background())
	if ok {
		t.Fatal("a stole live lease back")
	}
}
