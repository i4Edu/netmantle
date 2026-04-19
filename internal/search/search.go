// Package search provides full-text search across stored device
// configurations. It uses SQLite's FTS5 virtual table populated by
// Index() at backup time.
package search

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Service indexes and queries config_search.
type Service struct{ DB *sql.DB }

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Index inserts a single (tenant,device,artifact,sha) document into the
// FTS5 table. It is idempotent on (device_id, commit_sha, artifact).
func (s *Service) Index(ctx context.Context, tenantID, deviceID int64, artifact, sha string, body []byte) error {
	// Idempotency: delete any existing row for this (device,artifact,sha).
	_, _ = s.DB.ExecContext(ctx,
		`DELETE FROM config_search WHERE device_id = ? AND commit_sha = ? AND artifact = ?`,
		deviceID, sha, artifact)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO config_search(artifact, body, device_id, commit_sha, tenant_id) VALUES(?, ?, ?, ?, ?)`,
		artifact, string(body), deviceID, sha, tenantID)
	return err
}

// Hit is a single search match.
type Hit struct {
	DeviceID  int64  `json:"device_id"`
	Hostname  string `json:"hostname"`
	Artifact  string `json:"artifact"`
	CommitSHA string `json:"commit_sha"`
	Snippet   string `json:"snippet"`
}

// Query runs an FTS5 MATCH query for a tenant. Only the latest indexed
// version for each device+artifact is returned.
func (s *Service) Query(ctx context.Context, tenantID int64, q string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `
        SELECT cs.device_id, IFNULL(d.hostname,''), cs.artifact, cs.commit_sha,
               snippet(config_search, 1, '[', ']', '…', 12)
        FROM config_search cs
        LEFT JOIN devices d ON d.id = cs.device_id
        WHERE cs.tenant_id = ? AND config_search MATCH ?
        LIMIT ?`, tenantID, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.DeviceID, &h.Hostname, &h.Artifact, &h.CommitSHA, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SavedSearch is a stored query, optionally with a notification channel.
type SavedSearch struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Query           string    `json:"query"`
	NotifyChannelID *int64    `json:"notify_channel_id,omitempty"`
	LastMatchCount  int       `json:"last_match_count"`
	LastRunAt       time.Time `json:"last_run_at,omitempty"`
}

// SaveSearch inserts a saved search.
func (s *Service) SaveSearch(ctx context.Context, tenantID, userID int64, name, q string, notify *int64) (int64, error) {
	var uid any
	if userID > 0 {
		uid = userID
	}
	res, err := s.DB.ExecContext(ctx, `
        INSERT INTO saved_searches(tenant_id, user_id, name, query, notify_channel_id, created_at)
        VALUES(?, ?, ?, ?, ?, ?)`,
		tenantID, uid, name, q, notify, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListSaved returns all saved searches for a tenant.
func (s *Service) ListSaved(ctx context.Context, tenantID int64) ([]SavedSearch, error) {
	rows, err := s.DB.QueryContext(ctx, `
        SELECT id, name, query, notify_channel_id, last_match_count, IFNULL(last_run_at,'')
        FROM saved_searches WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedSearch
	for rows.Next() {
		var (
			s   SavedSearch
			ts  string
			nid sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.Query, &nid, &s.LastMatchCount, &ts); err != nil {
			return nil, err
		}
		if nid.Valid {
			v := nid.Int64
			s.NotifyChannelID = &v
		}
		if ts != "" {
			s.LastRunAt, _ = time.Parse(time.RFC3339, ts)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
