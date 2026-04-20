package poller_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/poller"
	"github.com/i4Edu/netmantle/internal/storage"
)

const claimRetryMaxAttempts = 500

func BenchmarkClaimLatency1000Pollers10000Queue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, p95 := runClaimScaleScenario(b, 1000, 10000)
		b.ReportMetric(float64(p95.Milliseconds()), "p95_ms")
	}
}

func TestConcurrentClaimNoDuplicateOwnershipAtScale(t *testing.T) {
	claimed, _ := runClaimScaleScenario(t, 1000, 10000)
	seen := make(map[int64]struct{}, len(claimed))
	for _, id := range claimed {
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate claim detected for job_id=%d", id)
		}
		seen[id] = struct{}{}
	}
	if len(claimed) != 1000 {
		t.Fatalf("expected 1000 claimed jobs, got %d", len(claimed))
	}
}

type tb interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

func runClaimScaleScenario(t tb, pollerCount, queueSize int) ([]int64, time.Duration) {
	t.Helper()
	dsn := fmt.Sprintf("file:poller-scale-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := storage.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(16)
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	jobs := poller.NewJobService(db)
	if err := seedScaleTenantDevice(db); err != nil {
		t.Fatal(err)
	}
	if err := seedScalePollers(db, pollerCount); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < queueSize; i++ {
		if _, err := jobs.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, `{}`, fmt.Sprintf("scale-%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		durations = make([]time.Duration, 0, pollerCount)
		claimed   = make([]int64, 0, pollerCount)
		errCh     = make(chan error, pollerCount)
	)
	for id := 1; id <= pollerCount; id++ {
		id := int64(id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			var (
				job poller.Job
				err error
			)
			for attempt := 0; attempt < claimRetryMaxAttempts; attempt++ {
				job, err = jobs.Claim(context.Background(), 1, id, []poller.JobType{poller.JobTypeBackup})
				if err == nil {
					break
				}
				if err == sql.ErrNoRows {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				errCh <- fmt.Errorf("claim failed for poller %d: %w", id, err)
				return
			}
			if err != nil {
				errCh <- fmt.Errorf("claim retries exhausted for poller %d: %w", id, err)
				return
			}
			elapsed := time.Since(start)
			mu.Lock()
			durations = append(durations, elapsed)
			claimed = append(claimed, job.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(durations) == 0 {
		return claimed, 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95)/100]
	return claimed, p95
}

func seedScaleTenantDevice(db *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO tenants(id, name, created_at) VALUES(1, 'scale', ?)`, now); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO devices(id, tenant_id, hostname, address, port, driver, created_at) VALUES(10, 1, 'scale-device', '127.0.0.1', 22, 'generic_ssh', ?)`, now)
	return err
}

func seedScalePollers(db *sql.DB, n int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 1; i <= n; i++ {
		if _, err := db.Exec(`INSERT INTO pollers(id, tenant_id, zone, name, token_hash, created_at) VALUES(?, 1, 'scale', ?, 'hash', ?)`,
			i, fmt.Sprintf("poller-%d", i), now); err != nil {
			return err
		}
	}
	return nil
}
