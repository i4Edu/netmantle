package poller

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// JobStatus mirrors the CHECK constraint in the poller_jobs schema.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobClaimed   JobStatus = "claimed"
	JobRunning   JobStatus = "running"
	JobDone      JobStatus = "done"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// JobType mirrors the CHECK constraint in the poller_jobs schema.
type JobType string

const (
	JobTypeBackup JobType = "backup"
	JobTypeProbe  JobType = "probe"
	JobTypeCustom JobType = "custom"
)

// Job is a unit of work dispatched from the core to a remote poller.
type Job struct {
	ID             int64      `json:"id"`
	TenantID       int64      `json:"tenant_id"`
	PollerID       *int64     `json:"poller_id,omitempty"`
	IdempotencyKey string     `json:"idempotency_key"`
	DeviceID       int64      `json:"device_id"`
	JobType        JobType    `json:"job_type"`
	Payload        string     `json:"payload,omitempty"` // JSON
	Status         JobStatus  `json:"status"`
	ClaimedAt      *time.Time `json:"claimed_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	Result         string     `json:"result,omitempty"` // JSON
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// JobService manages the poller job queue in the `poller_jobs` table.
type JobService struct{ DB *sql.DB }

// NewJobService constructs a JobService.
func NewJobService(db *sql.DB) *JobService { return &JobService{DB: db} }

// Enqueue inserts a job. If idempotencyKey is empty a random one is
// generated. If a job with the same key already exists, the existing job
// is returned without inserting a duplicate.
func (s *JobService) Enqueue(ctx context.Context, tenantID, deviceID int64, jobType JobType, payloadJSON, idempotencyKey string, ttl time.Duration) (Job, error) {
	if tenantID <= 0 || deviceID <= 0 {
		return Job{}, errors.New("poller: tenant_id and device_id required")
	}
	if jobType == "" {
		jobType = JobTypeBackup
	}
	if idempotencyKey == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return Job{}, fmt.Errorf("poller: generate idempotency key: %w", err)
		}
		idempotencyKey = hex.EncodeToString(b)
	}

	now := time.Now().UTC()
	var expiresAt *string
	if ttl > 0 {
		s := now.Add(ttl).Format(time.RFC3339)
		expiresAt = &s
	}

	// Idempotent insert: if the key already exists return the existing row.
	res, err := s.DB.ExecContext(ctx, `
        INSERT INTO poller_jobs(tenant_id, device_id, idempotency_key, job_type, payload, status, created_at, expires_at)
        VALUES(?, ?, ?, ?, ?, 'queued', ?, ?)
        ON CONFLICT(idempotency_key) DO NOTHING`,
		tenantID, deviceID, idempotencyKey, string(jobType), payloadJSON,
		now.Format(time.RFC3339), expiresAt)
	if err != nil {
		return Job{}, fmt.Errorf("poller: enqueue: %w", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		// Conflict path: fetch the existing row.
		return s.GetByKey(ctx, idempotencyKey)
	}
	return s.Get(ctx, tenantID, id)
}

// Claim atomically marks a queued job as claimed by pollerID.
// Returns the job or sql.ErrNoRows if the queue is empty (or no matching
// job type is available for this poller's zone).
func (s *JobService) Claim(ctx context.Context, tenantID, pollerID int64, supportedTypes []JobType) (Job, error) {
	// Build a type IN clause.
	if len(supportedTypes) == 0 {
		supportedTypes = []JobType{JobTypeBackup, JobTypeProbe, JobTypeCustom}
	}
	// SELECT the best candidate first (LIMIT 1), fetching the timestamps
	// needed to recalculate expires_at in Go (avoids complex SQL arithmetic).
	var (
		jobID        int64
		createdAtStr string
		expiresAtStr sql.NullString
	)
	q := `SELECT id, created_at, expires_at FROM poller_jobs
          WHERE tenant_id=? AND status='queued'
            AND job_type IN (`
	args := []any{tenantID}
	for i, t := range supportedTypes {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, string(t))
	}
	q += `) ORDER BY created_at ASC LIMIT 1`
	err := s.DB.QueryRowContext(ctx, q, args...).Scan(&jobID, &createdAtStr, &expiresAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, sql.ErrNoRows
	}
	if err != nil {
		return Job{}, fmt.Errorf("poller: claim select: %w", err)
	}

	// Compute the new expires_at in Go for clarity and testability.
	// If the job had an expiry set, shift it to claimed_at + original TTL.
	claimedAt := time.Now().UTC()
	var newExpiresAt *string
	if expiresAtStr.Valid {
		newExpiresAt = computeNewExpiresAt(createdAtStr, expiresAtStr.String, claimedAt)
	}

	var expiresAtArg any
	if newExpiresAt != nil {
		expiresAtArg = *newExpiresAt
	}

	res, err := s.DB.ExecContext(ctx,
		`UPDATE poller_jobs
         SET status='claimed',
             poller_id=?,
             claimed_at=?,
             expires_at=?
         WHERE id=? AND tenant_id=? AND status='queued'
           AND EXISTS (
               SELECT 1 FROM pollers
               WHERE id=? AND tenant_id=?
           )`,
		pollerID, claimedAt.Format(time.RFC3339), expiresAtArg,
		jobID, tenantID, pollerID, tenantID)
	if err != nil {
		return Job{}, fmt.Errorf("poller: claim update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Another poller raced us, the job is no longer queued, or the poller
		// does not belong to this tenant; no job available.
		return Job{}, sql.ErrNoRows
	}
	return s.Get(ctx, tenantID, jobID)
}

// computeNewExpiresAt shifts an expiry timestamp to be relative to a new
// baseline (claimedAt) rather than the original createdAt.
// It preserves the original TTL duration (expiresAt - createdAt).
// Returns nil if the timestamps cannot be parsed or the TTL is non-positive.
func computeNewExpiresAt(createdAtStr, expiresAtStr string, claimedAt time.Time) *string {
	createdAt, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		return nil
	}
	ttl := expiresAt.Sub(createdAt)
	if ttl <= 0 {
		return nil
	}
	s := claimedAt.Add(ttl).UTC().Format(time.RFC3339)
	return &s
}

// Complete marks a claimed job as done or failed.
func (s *JobService) Complete(ctx context.Context, tenantID, jobID int64, success bool, resultJSON, errMsg string) error {
	status := string(JobDone)
	if !success {
		status = string(JobFailed)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB.ExecContext(ctx,
		`UPDATE poller_jobs SET status=?, result=?, error=?, completed_at=?
         WHERE tenant_id=? AND id=? AND status IN ('claimed','running')`,
		status, nullIfEmptyJob(resultJSON), nullIfEmptyJob(errMsg), now, tenantID, jobID)
	return err
}

// Cancel marks a queued or claimed job as cancelled.
func (s *JobService) Cancel(ctx context.Context, tenantID, jobID int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE poller_jobs SET status='cancelled', completed_at=?
         WHERE tenant_id=? AND id=? AND status IN ('queued','claimed')`,
		time.Now().UTC().Format(time.RFC3339), tenantID, jobID)
	return err
}

// ReclaimExpired resets claimed jobs whose expires_at has passed back to
// queued so they can be retried by another poller. Called by the scheduler.
func (s *JobService) ReclaimExpired(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `
        UPDATE poller_jobs SET status='queued', poller_id=NULL, claimed_at=NULL
        WHERE status='claimed' AND expires_at IS NOT NULL AND expires_at < ?`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Get fetches one job by ID within a tenant.
func (s *JobService) Get(ctx context.Context, tenantID, id int64) (Job, error) {
	row := s.DB.QueryRowContext(ctx, `
        SELECT id, tenant_id, poller_id, idempotency_key, device_id, job_type,
               IFNULL(payload,''), status, claimed_at, completed_at,
               IFNULL(result,''), IFNULL(error,''), created_at, expires_at
        FROM poller_jobs WHERE tenant_id=? AND id=?`, tenantID, id)
	return scanJob(row)
}

// GetByKey fetches a job by its idempotency key.
func (s *JobService) GetByKey(ctx context.Context, key string) (Job, error) {
	row := s.DB.QueryRowContext(ctx, `
        SELECT id, tenant_id, poller_id, idempotency_key, device_id, job_type,
               IFNULL(payload,''), status, claimed_at, completed_at,
               IFNULL(result,''), IFNULL(error,''), created_at, expires_at
        FROM poller_jobs WHERE idempotency_key=?`, key)
	return scanJob(row)
}

// ListByTenant returns jobs for a tenant, most recent first.
func (s *JobService) ListByTenant(ctx context.Context, tenantID int64, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
        SELECT id, tenant_id, poller_id, idempotency_key, device_id, job_type,
               IFNULL(payload,''), status, claimed_at, completed_at,
               IFNULL(result,''), IFNULL(error,''), created_at, expires_at
        FROM poller_jobs WHERE tenant_id=? ORDER BY created_at DESC LIMIT ?`,
		tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner) (Job, error) {
	var (
		j                      Job
		pollerID               sql.NullInt64
		claimedAt, completedAt sql.NullString
		expiresAt              sql.NullString
		createdAt              string
	)
	if err := s.Scan(
		&j.ID, &j.TenantID, &pollerID, &j.IdempotencyKey, &j.DeviceID,
		&j.JobType, &j.Payload, &j.Status,
		&claimedAt, &completedAt,
		&j.Result, &j.Error,
		&createdAt, &expiresAt,
	); err != nil {
		return Job{}, err
	}
	if pollerID.Valid {
		v := pollerID.Int64
		j.PollerID = &v
	}
	createdTime, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return Job{}, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	j.CreatedAt = createdTime
	if claimedAt.Valid {
		t, err := time.Parse(time.RFC3339, claimedAt.String)
		if err != nil {
			return Job{}, fmt.Errorf("parse claimed_at %q: %w", claimedAt.String, err)
		}
		j.ClaimedAt = &t
	}
	if completedAt.Valid {
		t, err := time.Parse(time.RFC3339, completedAt.String)
		if err != nil {
			return Job{}, fmt.Errorf("parse completed_at %q: %w", completedAt.String, err)
		}
		j.CompletedAt = &t
	}
	if expiresAt.Valid {
		t, err := time.Parse(time.RFC3339, expiresAt.String)
		if err != nil {
			return Job{}, fmt.Errorf("parse expires_at %q: %w", expiresAt.String, err)
		}
		j.ExpiresAt = &t
	}
	return j, nil
}

func nullIfEmptyJob(s string) any {
	if s == "" {
		return nil
	}
	return s
}
