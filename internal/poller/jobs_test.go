package poller_test

import (
"context"
"testing"
"time"

"github.com/i4Edu/netmantle/internal/poller"
"github.com/i4Edu/netmantle/internal/storage"
)

func newJobService(t *testing.T) (*poller.JobService, func()) {
t.Helper()
db, err := storage.Open("sqlite", ":memory:")
if err != nil {
t.Fatal(err)
}
if err := storage.Migrate(context.Background(), db); err != nil {
db.Close()
t.Fatal(err)
}
return poller.NewJobService(db), func() { db.Close() }
}

func seedTenant(t *testing.T, svc *poller.JobService, tenantID int64) {
t.Helper()
_, err := svc.DB.Exec(
`INSERT OR IGNORE INTO tenants(id, name, created_at) VALUES(?, ?, ?)`,
tenantID, "test-tenant", time.Now().UTC().Format(time.RFC3339))
if err != nil {
t.Fatalf("seed tenant: %v", err)
}
}

func seedDevice(t *testing.T, svc *poller.JobService, tenantID, deviceID int64) {
t.Helper()
_, err := svc.DB.Exec(
`INSERT OR IGNORE INTO devices(id, tenant_id, hostname, address, port, driver, created_at)
         VALUES(?, ?, 'test-host', '1.2.3.4', 22, 'generic_ssh', ?)`,
deviceID, tenantID, time.Now().UTC().Format(time.RFC3339))
if err != nil {
t.Fatalf("seed device: %v", err)
}
}

// seedPoller inserts a minimal poller row. The column name is token_hash (schema 0002).
func seedPoller(t *testing.T, svc *poller.JobService, tenantID, pollerID int64, name string) {
t.Helper()
_, err := svc.DB.Exec(
`INSERT OR IGNORE INTO pollers(id, tenant_id, zone, name, token_hash, created_at)
         VALUES(?, ?, 'default', ?, 'hash-placeholder', ?)`,
pollerID, tenantID, name, time.Now().UTC().Format(time.RFC3339))
if err != nil {
t.Fatalf("seed poller: %v", err)
}
}

func TestJobEnqueueAndGet(t *testing.T) {
svc, done := newJobService(t)
defer done()
seedTenant(t, svc, 1)
seedDevice(t, svc, 1, 10)

job, err := svc.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, `{"foo":"bar"}`, "key-abc", time.Hour)
if err != nil {
t.Fatalf("Enqueue: %v", err)
}
if job.Status != poller.JobQueued {
t.Fatalf("status: want queued got %s", job.Status)
}
if job.IdempotencyKey != "key-abc" {
t.Fatalf("idempotency key: %s", job.IdempotencyKey)
}

// Re-enqueue the same key — must return the same job (idempotent).
dup, err := svc.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, `{}`, "key-abc", time.Hour)
if err != nil {
t.Fatalf("re-enqueue: %v", err)
}
if dup.ID != job.ID {
t.Fatalf("expected same job ID on duplicate key: got %d want %d", dup.ID, job.ID)
}
}

func TestJobClaim(t *testing.T) {
svc, done := newJobService(t)
defer done()
seedTenant(t, svc, 1)
seedDevice(t, svc, 1, 10)
seedPoller(t, svc, 1, 1, "p1")

if _, err := svc.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, "", "claim-key", 0); err != nil {
t.Fatal(err)
}

claimed, err := svc.Claim(context.Background(), 1, 1, nil)
if err != nil {
t.Fatalf("Claim: %v", err)
}
if claimed.Status != poller.JobClaimed {
t.Fatalf("status: want claimed got %s", claimed.Status)
}
if claimed.PollerID == nil || *claimed.PollerID != 1 {
t.Fatalf("poller_id not set correctly")
}
}

func TestJobClaimEmpty(t *testing.T) {
svc, done := newJobService(t)
defer done()
seedTenant(t, svc, 1)

_, err := svc.Claim(context.Background(), 1, 1, nil)
if err == nil {
t.Fatal("expected error on empty queue")
}
}

func TestJobComplete(t *testing.T) {
svc, done := newJobService(t)
defer done()
seedTenant(t, svc, 1)
seedDevice(t, svc, 1, 10)
seedPoller(t, svc, 1, 2, "p2")

job, err := svc.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, "", "comp-key", 0)
if err != nil {
t.Fatal(err)
}
if _, err := svc.Claim(context.Background(), 1, 2, nil); err != nil {
t.Fatalf("Claim: %v", err)
}

if err := svc.Complete(context.Background(), 1, job.ID, true, `{"sha":"abc"}`, ""); err != nil {
t.Fatalf("Complete: %v", err)
}
finished, err := svc.Get(context.Background(), 1, job.ID)
if err != nil {
t.Fatal(err)
}
if finished.Status != poller.JobDone {
t.Fatalf("status: want done got %s", finished.Status)
}
}

func TestJobReclaimExpired(t *testing.T) {
svc, done := newJobService(t)
defer done()
seedTenant(t, svc, 1)
seedDevice(t, svc, 1, 10)
seedPoller(t, svc, 1, 3, "p3")

// Enqueue with a generous TTL, then manually backdate expires_at to avoid
// relying on sub-second RFC3339 precision in the comparison.
job, err := svc.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, "", "exp-key", time.Hour)
if err != nil {
t.Fatal(err)
}
if _, err := svc.Claim(context.Background(), 1, 3, nil); err != nil {
t.Fatalf("Claim: %v", err)
}
// Force-expire: set expires_at to one hour in the past.
if _, err := svc.DB.Exec(`UPDATE poller_jobs SET expires_at=? WHERE id=?`,
time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), job.ID); err != nil {
t.Fatalf("force-expire: %v", err)
}

n, err := svc.ReclaimExpired(context.Background())
if err != nil {
t.Fatalf("ReclaimExpired: %v", err)
}
if n == 0 {
t.Fatal("expected at least one job reclaimed")
}
reclaimed, _ := svc.Get(context.Background(), 1, job.ID)
if reclaimed.Status != poller.JobQueued {
t.Fatalf("status after reclaim: want queued got %s", reclaimed.Status)
}
}

func TestJobListByTenant(t *testing.T) {
svc, done := newJobService(t)
defer done()
seedTenant(t, svc, 1)
seedDevice(t, svc, 1, 10)

for i := 0; i < 5; i++ {
if _, err := svc.Enqueue(context.Background(), 1, 10, poller.JobTypeBackup, "", "", 0); err != nil {
t.Fatalf("Enqueue %d: %v", i, err)
}
}
jobs, err := svc.ListByTenant(context.Background(), 1, 100)
if err != nil {
t.Fatal(err)
}
if len(jobs) != 5 {
t.Fatalf("expected 5 jobs, got %d", len(jobs))
}
}
