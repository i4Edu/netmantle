package scheduler

import (
	"context"
	"sync/atomic"
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

// TestConcurrentLeaseAcquisition verifies that only one of N concurrent callers
// wins the lease when it is available.
func TestConcurrentLeaseAcquisition(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var winners int32
	ch := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-ch
			l := NewLease(db, "concurrent", 30*time.Second)
			ok, _ := l.Acquire(context.Background())
			if ok {
				atomic.AddInt32(&winners, 1)
			}
		}()
	}
	close(ch) // release all goroutines simultaneously
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&winners); got != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", got)
	}
}

// TestLeaseRenewalKeepsHolder verifies that a holder can continuously renew
// its lease without being displaced.
func TestLeaseRenewalKeepsHolder(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	holder := NewLease(db, "renew", 10*time.Second)
	ok, _ := holder.Acquire(context.Background())
	if !ok {
		t.Fatal("initial acquire failed")
	}
	// Renew several times in quick succession.
	for i := 0; i < 5; i++ {
		ok, err := holder.Acquire(context.Background())
		if err != nil || !ok {
			t.Fatalf("renewal %d failed: err=%v ok=%v", i, err, ok)
		}
	}
	// A competitor must still not be able to steal the live lease.
	other := NewLease(db, "renew", 10*time.Second)
	ok, _ = other.Acquire(context.Background())
	if ok {
		t.Fatal("competitor stole live lease after renewals")
	}
}

// TestRunnerJobsUnderLeader verifies that a Runner executes its jobs while it
// holds the lease, and skips them when the lease is held by another node.
func TestRunnerJobsUnderLeader(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var execCount int32
	job := Job{
		Name:     "ping",
		Interval: time.Millisecond,
		Run: func(ctx context.Context) error {
			atomic.AddInt32(&execCount, 1)
			return nil
		},
	}

	lease := NewLease(db, "runner-test", 30*time.Second)
	runner := &Runner{Lease: lease, Jobs: []Job{job}}

	// Run for 2 seconds so the 1-second internal ticker fires at least once.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runner.Start(ctx, nil)

	if got := atomic.LoadInt32(&execCount); got == 0 {
		t.Fatal("expected job to run at least once while holding lease")
	}
}

// TestRunnerSkipsWithoutLease verifies that a Runner does not execute jobs
// when a competing node holds the lease.
func TestRunnerSkipsWithoutLease(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	// Pre-acquire the lease with a long TTL so the runner can never win it.
	blocker := NewLease(db, "skip-test", 10*time.Minute)
	if ok, _ := blocker.Acquire(context.Background()); !ok {
		t.Fatal("blocker acquire failed")
	}

	var execCount int32
	job := Job{
		Name:     "skipped",
		Interval: time.Millisecond,
		Run: func(ctx context.Context) error {
			atomic.AddInt32(&execCount, 1)
			return nil
		},
	}

	lease := NewLease(db, "skip-test", 30*time.Second)
	runner := &Runner{Lease: lease, Jobs: []Job{job}}

	// Run for 2 seconds to match the runner ticker interval.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runner.Start(ctx, nil)

	if got := atomic.LoadInt32(&execCount); got != 0 {
		t.Fatalf("job ran %d times despite not holding lease", got)
	}
}

// TestTenantIsolationLeases verifies that leases for different tenants are
// independent — one tenant's scheduler does not interfere with another's.
func TestTenantIsolationLeases(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	t1 := NewLease(db, "tenant:1:backups", 30*time.Second)
	t2 := NewLease(db, "tenant:2:backups", 30*time.Second)

	ok, _ := t1.Acquire(context.Background())
	if !ok {
		t.Fatal("tenant 1 acquire failed")
	}
	ok, _ = t2.Acquire(context.Background())
	if !ok {
		t.Fatal("tenant 2 acquire failed — must be independent of tenant 1")
	}

	// Both tenants can renew without conflict.
	for i := 0; i < 3; i++ {
		if ok, _ := t1.Acquire(context.Background()); !ok {
			t.Fatalf("tenant 1 renewal %d failed", i)
		}
		if ok, _ := t2.Acquire(context.Background()); !ok {
			t.Fatalf("tenant 2 renewal %d failed", i)
		}
	}
}
