// Package scheduler implements a small leader-elected job runner backed
// by a SQLite-row lease (Phase 9). Multiple replicas of the core can run
// safely; only the lease holder will fire scheduled jobs.
package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"sync"
	"time"
)

// Lease is a renewable named lock.
type Lease struct {
	DB     *sql.DB
	Name   string
	TTL    time.Duration
	Holder string
}

// NewLease returns a fresh Lease with a random holder ID.
func NewLease(db *sql.DB, name string, ttl time.Duration) *Lease {
	if ttl < 5*time.Second {
		ttl = 30 * time.Second
	}
	return &Lease{DB: db, Name: name, TTL: ttl, Holder: randID()}
}

// Acquire attempts to take or renew the lease atomically. Returns true on
// success.
func (l *Lease) Acquire(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	expires := now.Add(l.TTL)
	// Try to insert.
	if _, err := l.DB.ExecContext(ctx,
		`INSERT INTO scheduler_leases(name, holder, expires_at) VALUES(?, ?, ?)`,
		l.Name, l.Holder, expires.Format(time.RFC3339)); err == nil {
		return true, nil
	}
	// Update if expired or already ours.
	res, err := l.DB.ExecContext(ctx, `
        UPDATE scheduler_leases SET holder=?, expires_at=?
        WHERE name=? AND (holder=? OR expires_at < ?)`,
		l.Holder, expires.Format(time.RFC3339),
		l.Name, l.Holder, now.Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Job is a scheduled function.
type Job struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error
}

// Runner runs a set of Jobs while holding a single named Lease.
type Runner struct {
	Lease        *Lease
	Jobs         []Job
	TickInterval time.Duration // defaults to 1s when zero
}

// Start blocks until ctx is cancelled. While the lease is held, jobs are
// invoked at their interval; otherwise they are skipped.
func (r *Runner) Start(ctx context.Context, log func(string, ...any)) {
	if log == nil {
		log = func(string, ...any) {}
	}
	tickInterval := r.TickInterval
	if tickInterval <= 0 {
		tickInterval = time.Second
	}
	last := make(map[string]time.Time, len(r.Jobs))
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			ok, err := r.Lease.Acquire(ctx)
			if err != nil {
				log("scheduler: acquire failed", "err", err)
				continue
			}
			if !ok {
				continue
			}
			var wg sync.WaitGroup
			for _, j := range r.Jobs {
				if !last[j.Name].IsZero() && now.Sub(last[j.Name]) < j.Interval {
					continue
				}
				j := j
				last[j.Name] = now
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := j.Run(ctx); err != nil {
						log("scheduler: job error", "job", j.Name, "err", err)
					}
				}()
			}
			wg.Wait()
		}
	}
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
