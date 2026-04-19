// Package changes records and queries config-version changes (Phase 2).
package changes

import (
	"context"
	"database/sql"
	"time"

	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/diff"
)

// Event is a recorded change between two config versions.
type Event struct {
	ID        int64     `json:"id"`
	DeviceID  int64     `json:"device_id"`
	Artifact  string    `json:"artifact"`
	OldSHA    string    `json:"old_sha,omitempty"`
	NewSHA    string    `json:"new_sha"`
	Added     int       `json:"added_lines"`
	Removed   int       `json:"removed_lines"`
	Reviewed  bool      `json:"reviewed"`
	CreatedAt time.Time `json:"created_at"`
}

// Service writes change events and exposes diff queries.
type Service struct {
	DB     *sql.DB
	Store  *configstore.Store
	Engine *diff.Engine
}

// New constructs a Service.
func New(db *sql.DB, s *configstore.Store, e *diff.Engine) *Service {
	if e == nil {
		e = &diff.Engine{Rules: diff.DefaultRules()}
	}
	return &Service{DB: db, Store: s, Engine: e}
}

// Record evaluates the diff for an artifact in a device's repo and, if any
// non-ignored content actually changed, inserts a row in change_events.
// Returns the event (or nil when there was no change to record).
func (s *Service) Record(ctx context.Context, tenantID, deviceID int64, artifact, newSHA string) (*Event, error) {
	// Find the previous SHA (the immediate predecessor).
	var oldSHA sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT commit_sha FROM config_versions WHERE device_id=? AND artifact=? AND commit_sha<>? ORDER BY created_at DESC LIMIT 1`,
		deviceID, artifact, newSHA,
	).Scan(&oldSHA); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	newBody, err := s.Store.Read(tenantID, deviceID, artifact, newSHA)
	if err != nil {
		return nil, err
	}
	var oldBody []byte
	if oldSHA.Valid {
		if b, err := s.Store.Read(tenantID, deviceID, artifact, oldSHA.String); err == nil {
			oldBody = b
		}
	}
	res := s.Engine.Diff(artifact, string(oldBody), string(newBody))
	if res.Identical {
		return nil, nil
	}
	now := time.Now().UTC()
	r, err := s.DB.ExecContext(ctx,
		`INSERT INTO change_events(device_id, artifact, old_sha, new_sha, added_lines, removed_lines, reviewed, created_at) VALUES(?, ?, ?, ?, ?, ?, 0, ?)`,
		deviceID, artifact, nullableString(oldSHA), newSHA, res.AddedLines, res.RemovedLines, now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	return &Event{
		ID: id, DeviceID: deviceID, Artifact: artifact,
		OldSHA: oldSHA.String, NewSHA: newSHA,
		Added: res.AddedLines, Removed: res.RemovedLines,
		Reviewed: false, CreatedAt: now,
	}, nil
}

// List returns recent change events for a tenant (most recent first).
func (s *Service) List(ctx context.Context, tenantID int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
        SELECT ce.id, ce.device_id, ce.artifact, IFNULL(ce.old_sha,''), ce.new_sha,
               ce.added_lines, ce.removed_lines, ce.reviewed, ce.created_at
        FROM change_events ce
        JOIN devices d ON d.id = ce.device_id
        WHERE d.tenant_id = ?
        ORDER BY ce.created_at DESC
        LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e        Event
			reviewed int
			ts       string
		)
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.Artifact, &e.OldSHA, &e.NewSHA,
			&e.Added, &e.Removed, &reviewed, &ts); err != nil {
			return nil, err
		}
		e.Reviewed = reviewed == 1
		e.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Diff returns the unified diff for a single change event.
func (s *Service) Diff(ctx context.Context, tenantID, eventID int64) (string, error) {
	var (
		deviceID    int64
		artifact    string
		oldSHA, new string
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT device_id, artifact, IFNULL(old_sha,''), new_sha FROM change_events WHERE id=?`, eventID,
	).Scan(&deviceID, &artifact, &oldSHA, &new)
	if err != nil {
		return "", err
	}
	// Tenant gate.
	var tid int64
	if err := s.DB.QueryRowContext(ctx, `SELECT tenant_id FROM devices WHERE id=?`, deviceID).Scan(&tid); err != nil {
		return "", err
	}
	if tid != tenantID {
		return "", sql.ErrNoRows
	}
	newBody, err := s.Store.Read(tenantID, deviceID, artifact, new)
	if err != nil {
		return "", err
	}
	var oldBody []byte
	if oldSHA != "" {
		if b, err := s.Store.Read(tenantID, deviceID, artifact, oldSHA); err == nil {
			oldBody = b
		}
	}
	return s.Engine.Diff(artifact, string(oldBody), string(newBody)).Unified, nil
}

// MarkReviewed flips the reviewed flag.
func (s *Service) MarkReviewed(ctx context.Context, tenantID, eventID int64) error {
	_, err := s.DB.ExecContext(ctx, `
        UPDATE change_events SET reviewed = 1
        WHERE id = ? AND device_id IN (SELECT id FROM devices WHERE tenant_id = ?)`,
		eventID, tenantID)
	return err
}

func nullableString(n sql.NullString) any {
	if !n.Valid {
		return nil
	}
	return n.String
}
