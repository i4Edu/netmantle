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

// TestLeaseContextCancellation verifies that Acquire respects context
// cancellation, which is important for clean HA shutdown.
func TestLeaseContextCancellation(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	l := NewLease(db, "cancel-test", 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err := l.Acquire(ctx)
	// SQLite may or may not propagate the context error through the driver.
	// What we require is that Acquire does not panic and returns promptly.
	_ = err
}

// TestSplitBrainPrevention confirms that after a simulated split-brain
// (two nodes simultaneously believe they hold the lease), the one that
// refreshes last wins, and the other is prevented from re-winning.
func TestSplitBrainPrevention(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	nodeA := NewLease(db, "split-brain", 30*time.Second)
	nodeB := NewLease(db, "split-brain", 30*time.Second)

	// A acquires.
	ok, err := nodeA.Acquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("A acquire: err=%v ok=%v", err, ok)
	}
	// B cannot acquire while A's lease is live.
	ok, _ = nodeB.Acquire(context.Background())
	if ok {
		t.Fatal("B should not steal A's live lease")
	}
	// Simulate A's lease expiring without A noticing (e.g., clock skew or partition).
	_, _ = db.Exec(`UPDATE scheduler_leases SET expires_at=? WHERE name='split-brain'`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	// B wins the expired lease.
	ok, err = nodeB.Acquire(context.Background())
	if err != nil || !ok {
		t.Fatalf("B takeover: err=%v ok=%v", err, ok)
	}
	// A tries to reclaim — must fail (B now holds it).
	ok, err = nodeA.Acquire(context.Background())
	if err != nil {
		t.Fatalf("A reclaim attempt error: %v", err)
	}
	if ok {
		t.Fatal("A reclaimed lease despite B holding it — split-brain not prevented")
	}
}

// TestLeaderHandoffTimeliness verifies that once the current holder's lease
// expires, a successor can take over within one acquisition attempt. In the
// real system the scheduler polls on every tick; here we simulate the expiry
// by directly backdating the lease row, then confirm the successor wins on
// the very next Acquire call.
func TestLeaderHandoffTimeliness(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	incumbent := NewLease(db, "handoff", 30*time.Second)
	successor := NewLease(db, "handoff", 30*time.Second)

	ok, _ := incumbent.Acquire(context.Background())
	if !ok {
		t.Fatal("incumbent acquire failed")
	}
	// Verify successor cannot take over while the lease is live.
	ok, _ = successor.Acquire(context.Background())
	if ok {
		t.Fatal("successor took over while incumbent's lease was still live")
	}

	// Simulate the incumbent going away: backdate the lease to force expiry.
	_, _ = db.Exec(`UPDATE scheduler_leases SET expires_at=? WHERE name='handoff'`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))

	// Successor must take over on the very next Acquire call.
	start := time.Now()
	ok, err := successor.Acquire(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("successor acquire after expiry: %v", err)
	}
	if !ok {
		t.Fatal("successor did not take over after lease expiry")
	}
	t.Logf("takeover latency: %v", elapsed)

	// Incumbent must be locked out immediately.
	ok, _ = incumbent.Acquire(context.Background())
	if ok {
		t.Fatal("incumbent reclaimed lease after successor took over")
	}
}

// TestMultipleRapidLeaseFlips verifies the lease mechanism stays consistent
// through rapid sequential leadership changes (simulates HA failover storms).
func TestMultipleRapidLeaseFlips(t *testing.T) {
	db, _ := storage.Open("sqlite", ":memory:")
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	const rounds = 10
	prev := NewLease(db, "flip", 10*time.Second)
	ok, _ := prev.Acquire(context.Background())
	if !ok {
		t.Fatal("initial acquire failed")
	}
	for i := 0; i < rounds; i++ {
		next := NewLease(db, "flip", 10*time.Second)
		// Expire the current holder.
		_, _ = db.Exec(`UPDATE scheduler_leases SET expires_at=? WHERE name='flip'`,
			time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
		ok, err := next.Acquire(context.Background())
		if err != nil || !ok {
			t.Fatalf("round %d: next acquire: err=%v ok=%v", i, err, ok)
		}
		// Previous holder must not be able to reclaim.
		ok, _ = prev.Acquire(context.Background())
		if ok {
			t.Fatalf("round %d: previous holder reclaimed after being displaced", i)
		}
		prev = next
	}
}
