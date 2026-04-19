// Package audit provides a single helper used by mutating handlers to write
// rows to the append-only audit_log table. The schema itself was created in
// migration 0001 (tenant_id/user_id/action/target/detail/created_at) and
// extended in 0003 with actor_user_id + source so that every mutating call
// site uses uniform fields.
//
// The Service deliberately swallows write errors after logging them: an
// audit-write failure must never break the user-visible action. In tests,
// callers can read directly from the audit_log table.
package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"
)

// Source enumerates the channel that initiated an action. Free-form text in
// the column to allow future kinds; constants keep the common values
// consistent.
const (
	SourceUI        = "ui"
	SourceAPI       = "api"
	SourceScheduler = "scheduler"
	SourcePoller    = "poller"
	SourceSystem    = "system"
)

// Service writes audit rows. It is safe for concurrent use.
type Service struct {
	DB     *sql.DB
	Logger *slog.Logger
}

// New constructs a Service.
func New(db *sql.DB, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{DB: db, Logger: logger}
}

// Entry is a single audit_log row as returned by List.
type Entry struct {
	ID          int64     `json:"id"`
	TenantID    *int64    `json:"tenant_id,omitempty"`
	ActorUserID *int64    `json:"actor_user_id,omitempty"`
	Action      string    `json:"action"`
	Target      string    `json:"target,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Source      string    `json:"source,omitempty"`
	RequestID   string    `json:"request_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Record inserts a new audit_log row. Errors are logged and swallowed so
// that an audit-write failure never breaks the user-visible action.
//
// tenantID and actorUserID may be 0 to record a NULL (e.g. for
// system-initiated actions before a tenant is established).
func (s *Service) Record(ctx context.Context, tenantID, actorUserID int64, source, action, target, detail string) {
	s.RecordWithRequest(ctx, tenantID, actorUserID, "", source, action, target, detail)
}

// RecordWithRequest is like Record but also stores a per-request
// correlation id (typically the X-Request-ID header value). Use this
// from API handlers so every audit row a single HTTP call produces can
// be retrieved as a coherent timeline.
func (s *Service) RecordWithRequest(ctx context.Context, tenantID, actorUserID int64, requestID, source, action, target, detail string) {
	if s == nil || s.DB == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var t, u, rid any
	if tenantID > 0 {
		t = tenantID
	}
	if actorUserID > 0 {
		u = actorUserID
	}
	if requestID != "" {
		rid = requestID
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO audit_log(tenant_id, user_id, actor_user_id, action, target, detail, source, request_id, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t, u, u, action, target, detail, source, rid, now); err != nil {
		s.Logger.Warn("audit write failed",
			"err", err, "action", action, "target", target)
	}
}

// ListFilter narrows a List query. Zero-valued fields are ignored.
type ListFilter struct {
	TenantID    int64
	ActorUserID int64
	Action      string // exact match
	Target      string // substring (LIKE %target%)
	RequestID   string // exact match on request_id
	Since       time.Time
	Until       time.Time
	Limit       int
	Offset      int
}

// List returns audit entries newest-first matching the filter. The caller is
// responsible for tenant scoping (typically by setting TenantID).
func (s *Service) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	var (
		where []string
		args  []any
	)
	if f.TenantID > 0 {
		where = append(where, "tenant_id = ?")
		args = append(args, f.TenantID)
	}
	if f.ActorUserID > 0 {
		where = append(where, "actor_user_id = ?")
		args = append(args, f.ActorUserID)
	}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	if f.Target != "" {
		where = append(where, "target LIKE ?")
		args = append(args, "%"+f.Target+"%")
	}
	if f.RequestID != "" {
		where = append(where, "request_id = ?")
		args = append(args, f.RequestID)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since.UTC().Format(time.RFC3339))
	}
	if !f.Until.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, f.Until.UTC().Format(time.RFC3339))
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, tenant_id, actor_user_id, action,
	             COALESCE(target,''), COALESCE(detail,''), COALESCE(source,''),
	             COALESCE(request_id,''),
	             created_at
	      FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var (
			e        Entry
			tenantID sql.NullInt64
			actorID  sql.NullInt64
			ts       string
		)
		if err := rows.Scan(&e.ID, &tenantID, &actorID, &e.Action,
			&e.Target, &e.Detail, &e.Source, &e.RequestID, &ts); err != nil {
			return nil, err
		}
		if tenantID.Valid {
			v := tenantID.Int64
			e.TenantID = &v
		}
		if actorID.Valid {
			v := actorID.Int64
			e.ActorUserID = &v
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		if e.CreatedAt.IsZero() {
			// Tolerate any historical rows that may have been written
			// with sub-second precision before the format was unified.
			e.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
