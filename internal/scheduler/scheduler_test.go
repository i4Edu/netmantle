package scheduler

import (
	"context"
	"sync"
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
	ok, err = b.Acquire(context.Background())
	if err != nil {
		t.Fatalf("b acquire (live): %v", err)
	}
	if ok {
		t.Fatal("b stole live lease")
	}
	// Manually expire a's lease.
	_, _ = db.Exec(`UPDATE scheduler_leases SET expires_at=? WHERE name='backups'`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	ok, err = b.Acquire(context.Background())
	if err != nil {
		t.Fatalf("b acquire (expired): %v", err)
	}
	if !ok {
		t.Fatal("b should take over after expiry")
	}
	// a renewing should now fail (b holds).
	ok, err = a.Acquire(context.Background())
	if err != nil {
		t.Fatalf("a re-acquire: %v", err)
	}
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
	var (
		winners int32
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
	)
	ch := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ch
			l := NewLease(db, "concurrent", 30*time.Second)
			ok, err := l.Acquire(context.Background())
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			if ok {
				atomic.AddInt32(&winners, 1)
			}
		}()
	}
	close(ch) // release all goroutines simultaneously
	wg.Wait() // wait until every goroutine has finished before asserting
	for _, e := range errs {
		t.Errorf("Acquire returned unexpected error: %v", e)
	}
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
	ok, err := holder.Acquire(context.Background())
	if err != nil {
		t.Fatalf("initial acquire error: %v", err)
	}
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
	ok, err = other.Acquire(context.Background())
	if err != nil {
		t.Fatalf("competitor acquire error: %v", err)
	}
	if ok {
		t.Fatal("competitor stole live lease after renewals")
	}
}

// testTick is a small interval used by runner tests so they don't have to
// wait 1 second for the real default ticker to fire.
const testTick = 10 * time.Millisecond

// TestRunnerJobsUnderLeader verifies that a Runner executes its jobs while it
// holds the lease.
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
	runner := &Runner{Lease: lease, Jobs: []Job{job}, TickInterval: testTick}

	// Run until at least one tick has occurred, then stop.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
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
	ok, err := blocker.Acquire(context.Background())
	if err != nil {
		t.Fatalf("blocker acquire error: %v", err)
	}
	if !ok {
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
	runner := &Runner{Lease: lease, Jobs: []Job{job}, TickInterval: testTick}

	// Run for several ticks — the runner must never win the lease.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
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

	ok, err := t1.Acquire(context.Background())
	if err != nil {
		t.Fatalf("tenant 1 acquire error: %v", err)
	}
	if !ok {
		t.Fatal("tenant 1 acquire failed")
	}
	ok, err = t2.Acquire(context.Background())
	if err != nil {
		t.Fatalf("tenant 2 acquire error: %v", err)
	}
	if !ok {
		t.Fatal("tenant 2 acquire failed — must be independent of tenant 1")
	}

	// Both tenants can renew without conflict.
	for i := 0; i < 3; i++ {
		ok, err := t1.Acquire(context.Background())
		if err != nil {
			t.Fatalf("tenant 1 renewal %d error: %v", i, err)
		}
		if !ok {
			t.Fatalf("tenant 1 renewal %d failed", i)
		}
		ok, err = t2.Acquire(context.Background())
		if err != nil {
			t.Fatalf("tenant 2 renewal %d error: %v", i, err)
		}
		if !ok {
			t.Fatalf("tenant 2 renewal %d failed", i)
		}
	}
}
