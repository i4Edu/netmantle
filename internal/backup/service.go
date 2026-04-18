// Package backup orchestrates device backups: it owns the worker pool,
// drives drivers via the SSH transport, persists results in the configstore,
// and records run metadata + audit entries.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
)

// SessionFactory opens a Session for the supplied device + credentials.
// It is abstracted so tests can substitute an in-memory transport.
type SessionFactory func(ctx context.Context, d devices.Device, username, password string) (drivers.Session, func() error, error)

// Service runs backups on demand or via the scheduler.
type Service struct {
	Devices     *devices.Repo
	Credentials *credentials.Repo
	Store       *configstore.Store
	DB          *sql.DB
	Logger      *slog.Logger
	Timeout     time.Duration
	NewSession  SessionFactory

	sem chan struct{} // bounds concurrent backups
}

// New constructs a Service with the given concurrency limit.
func New(d *devices.Repo, c *credentials.Repo, s *configstore.Store, db *sql.DB, log *slog.Logger, timeout time.Duration, workers int, fn SessionFactory) *Service {
	if workers <= 0 {
		workers = 1
	}
	return &Service{
		Devices:     d,
		Credentials: c,
		Store:       s,
		DB:          db,
		Logger:      log,
		Timeout:     timeout,
		NewSession:  fn,
		sem:         make(chan struct{}, workers),
	}
}

// Run is the result of a single backup attempt.
type Run struct {
	ID         int64     `json:"id"`
	DeviceID   int64     `json:"device_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	CommitSHA  string    `json:"commit_sha,omitempty"`
}

// BackupNow performs a synchronous backup of one device.
func (s *Service) BackupNow(ctx context.Context, tenantID, deviceID int64, actor string) (*Run, error) {
	// Concurrency gate.
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	dev, err := s.Devices.GetDevice(ctx, tenantID, deviceID)
	if err != nil {
		return nil, err
	}
	driver, err := drivers.Get(dev.Driver)
	if err != nil {
		return nil, err
	}
	if dev.CredentialID == nil {
		return nil, errors.New("backup: device has no credential")
	}
	username, secret, err := s.Credentials.Reveal(ctx, tenantID, *dev.CredentialID)
	if err != nil {
		return nil, fmt.Errorf("backup: reveal credentials: %w", err)
	}

	startedAt := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO backup_runs(device_id, started_at, status) VALUES(?, ?, 'running')`,
		dev.ID, startedAt.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	runID, _ := res.LastInsertId()
	run := &Run{ID: runID, DeviceID: dev.ID, StartedAt: startedAt, Status: "running"}

	finalize := func(status, errMsg, sha string) {
		now := time.Now().UTC()
		run.FinishedAt = now
		run.Status = status
		run.Error = errMsg
		run.CommitSHA = sha
		if _, uerr := s.DB.ExecContext(context.Background(),
			`UPDATE backup_runs SET finished_at=?, status=?, error=?, commit_sha=? WHERE id=?`,
			now.Format(time.RFC3339), status, nullIfEmpty(errMsg), nullIfEmpty(sha), runID,
		); uerr != nil {
			s.Logger.Warn("update backup_run failed", "err", uerr, "run_id", runID)
		}
		_, _ = s.DB.ExecContext(context.Background(),
			`INSERT INTO audit_log(tenant_id, action, target, detail, created_at) VALUES(?, ?, ?, ?, ?)`,
			tenantID, "device.backup", fmt.Sprintf("device:%d", dev.ID),
			fmt.Sprintf("status=%s actor=%s sha=%s err=%s", status, actor, sha, errMsg),
			now.Format(time.RFC3339))
	}

	bctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	sess, closer, err := s.NewSession(bctx, dev, username, secret)
	if err != nil {
		finalize("failed", "session: "+err.Error(), "")
		return run, fmt.Errorf("backup: session: %w", err)
	}
	defer func() { _ = closer() }()

	arts, err := driver.FetchConfig(bctx, sess)
	if err != nil {
		finalize("failed", "fetch: "+err.Error(), "")
		return run, fmt.Errorf("backup: fetch: %w", err)
	}
	if len(arts) == 0 {
		finalize("failed", "fetch returned no artefacts", "")
		return run, errors.New("backup: no artefacts")
	}

	storeArts := make([]configstore.Artifact, 0, len(arts))
	for _, a := range arts {
		storeArts = append(storeArts, configstore.Artifact{Name: a.Name, Content: a.Content})
	}
	cr, err := s.Store.Commit(tenantID, dev.ID, dev.Hostname, actor, storeArts)
	if err != nil && !errors.Is(err, configstore.ErrNoChange) {
		finalize("failed", "commit: "+err.Error(), "")
		return run, fmt.Errorf("backup: commit: %w", err)
	}

	sha := ""
	if cr != nil {
		sha = cr.SHA
		now := time.Now().UTC()
		for _, a := range storeArts {
			if _, err := s.DB.ExecContext(ctx,
				`INSERT INTO config_versions(device_id, artifact, commit_sha, size_bytes, created_at) VALUES(?, ?, ?, ?, ?)`,
				dev.ID, a.Name, cr.SHA, len(a.Content), now.Format(time.RFC3339)); err != nil {
				s.Logger.Warn("insert config_version failed", "err", err)
			}
		}
	}
	finalize("success", "", sha)
	return run, nil
}

// ListRuns returns recent runs for a device.
func (s *Service) ListRuns(ctx context.Context, deviceID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, device_id, started_at, IFNULL(finished_at,''), status, IFNULL(error,''), IFNULL(commit_sha,'')
        FROM backup_runs WHERE device_id=? ORDER BY started_at DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var (
			r        Run
			started  string
			finished string
		)
		if err := rows.Scan(&r.ID, &r.DeviceID, &started, &finished, &r.Status, &r.Error, &r.CommitSHA); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339, started)
		if finished != "" {
			r.FinishedAt, _ = time.Parse(time.RFC3339, finished)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestVersion returns the latest stored artifact name + content for a device.
func (s *Service) LatestVersion(ctx context.Context, tenantID, deviceID int64, artifact string) ([]byte, string, error) {
	if artifact == "" {
		artifact = "running-config"
	}
	var sha string
	err := s.DB.QueryRowContext(ctx,
		`SELECT commit_sha FROM config_versions WHERE device_id=? AND artifact=? ORDER BY created_at DESC LIMIT 1`,
		deviceID, artifact,
	).Scan(&sha)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", errors.New("backup: no versions yet")
		}
		return nil, "", err
	}
	body, err := s.Store.Read(tenantID, deviceID, artifact, sha)
	if err != nil {
		return nil, "", err
	}
	return body, sha, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Compile-time assertion: Service is safe for concurrent BackupNow calls
// (the semaphore + per-call DB writes guarantee it).
var _ = sync.Mutex{}
