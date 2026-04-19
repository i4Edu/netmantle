// Package changereq implements the approval workflow that gates every
// change before it reaches a device.
//
// A ChangeRequest is the unit Senior staff (admins, in NetMantle's RBAC
// vocabulary) approve. Every push or rollback must be associated with
// an approved request before the executor will apply it; admins can
// short-circuit this with the "apply:direct" capability they already
// possess by virtue of being admins, but the request row is still
// created so accountability is preserved.
//
// State machine
//
//	draft     →  submitted
//	submitted →  approved | rejected | cancelled
//	approved  →  applied  | failed   | cancelled
//	applied / rejected / failed / cancelled are terminal.
package changereq

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind classifies a ChangeRequest.
type Kind string

const (
	KindPush     Kind = "push"
	KindRollback Kind = "rollback"
)

// Status enumerates the lifecycle states.
type Status string

const (
	StatusDraft     Status = "draft"
	StatusSubmitted Status = "submitted"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusApplied   Status = "applied"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// ChangeRequest is a single proposed change.
type ChangeRequest struct {
	ID             int64             `json:"id"`
	TenantID       int64             `json:"tenant_id"`
	Kind           Kind              `json:"kind"`
	Title          string            `json:"title"`
	Description    string            `json:"description,omitempty"`
	RequesterID    int64             `json:"requester_id"`
	ReviewerID     *int64            `json:"reviewer_id,omitempty"`
	Status         Status            `json:"status"`
	DecisionReason string            `json:"decision_reason,omitempty"`
	PushJobID      *int64            `json:"push_job_id,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	DeviceID       *int64            `json:"device_id,omitempty"`
	Artifact       string            `json:"artifact,omitempty"`
	TargetSHA      string            `json:"target_sha,omitempty"`
	Payload        string            `json:"payload,omitempty"`
	Result         string            `json:"result,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	SubmittedAt    *time.Time        `json:"submitted_at,omitempty"`
	DecidedAt      *time.Time        `json:"decided_at,omitempty"`
	AppliedAt      *time.Time        `json:"applied_at,omitempty"`
}

// Event is a single state-machine transition recorded for a request.
type Event struct {
	ID              int64     `json:"id"`
	ChangeRequestID int64     `json:"change_request_id"`
	ActorUserID     *int64    `json:"actor_user_id,omitempty"`
	FromStatus      string    `json:"from_status,omitempty"`
	ToStatus        string    `json:"to_status"`
	Note            string    `json:"note,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// Service exposes ChangeRequest CRUD + transitions.
type Service struct {
	DB *sql.DB
}

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Errors.
var (
	ErrNotFound          = errors.New("changereq: not found")
	ErrInvalidInput      = errors.New("changereq: invalid input")
	ErrInvalidTransition = errors.New("changereq: invalid state transition")
	ErrSelfApproval      = errors.New("changereq: requester may not approve their own request")
	ErrTerminal          = errors.New("changereq: request is in a terminal state")
)

// allowedTransitions is the authoritative state machine.
var allowedTransitions = map[Status]map[Status]bool{
	StatusDraft:     {StatusSubmitted: true, StatusCancelled: true},
	StatusSubmitted: {StatusApproved: true, StatusRejected: true, StatusCancelled: true},
	StatusApproved:  {StatusApplied: true, StatusFailed: true, StatusCancelled: true},
}

// IsTerminal reports whether s admits no further transitions.
func IsTerminal(s Status) bool {
	switch s {
	case StatusApplied, StatusRejected, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

func canTransition(from, to Status) bool {
	if to == "" {
		return false
	}
	allowed, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// Create inserts a new ChangeRequest in `draft` state. Tenant, kind,
// title and requester are mandatory; per-kind required fields are
// validated here so that callers (UI, REST) get a single error contract.
func (s *Service) Create(ctx context.Context, cr ChangeRequest) (ChangeRequest, error) {
	cr.Title = strings.TrimSpace(cr.Title)
	switch {
	case cr.TenantID <= 0:
		return ChangeRequest{}, fmt.Errorf("%w: tenant_id required", ErrInvalidInput)
	case cr.Title == "":
		return ChangeRequest{}, fmt.Errorf("%w: title required", ErrInvalidInput)
	case cr.RequesterID <= 0:
		return ChangeRequest{}, fmt.Errorf("%w: requester required", ErrInvalidInput)
	}
	switch cr.Kind {
	case KindPush:
		if cr.PushJobID == nil || *cr.PushJobID <= 0 {
			return ChangeRequest{}, fmt.Errorf("%w: push_job_id required for kind=push", ErrInvalidInput)
		}
	case KindRollback:
		if cr.DeviceID == nil || *cr.DeviceID <= 0 ||
			strings.TrimSpace(cr.Artifact) == "" ||
			strings.TrimSpace(cr.TargetSHA) == "" ||
			strings.TrimSpace(cr.Payload) == "" {
			return ChangeRequest{}, fmt.Errorf("%w: device_id, artifact, target_sha and payload required for kind=rollback", ErrInvalidInput)
		}
	default:
		return ChangeRequest{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidInput, cr.Kind)
	}
	cr.Status = StatusDraft
	now := time.Now().UTC()
	cr.CreatedAt = now
	cr.UpdatedAt = now
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO change_requests(
			tenant_id, kind, title, description, requester_id, status,
			push_job_id, variables, device_id, artifact, target_sha, payload,
			created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cr.TenantID, string(cr.Kind), cr.Title, nullableStr(cr.Description), cr.RequesterID, string(cr.Status),
		nullableInt(cr.PushJobID), nullableJSON(cr.Variables), nullableInt(cr.DeviceID),
		nullableStr(cr.Artifact), nullableStr(cr.TargetSHA), nullableStr(cr.Payload),
		now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return ChangeRequest{}, err
	}
	id, _ := res.LastInsertId()
	cr.ID = id
	if err := s.recordEvent(ctx, id, cr.RequesterID, "", string(StatusDraft), "created"); err != nil {
		return ChangeRequest{}, err
	}
	return cr, nil
}

// Get fetches a ChangeRequest by id, scoped to a tenant.
func (s *Service) Get(ctx context.Context, tenantID, id int64) (ChangeRequest, error) {
	row := s.DB.QueryRowContext(ctx, selectColumns+`
		FROM change_requests WHERE tenant_id=? AND id=?`, tenantID, id)
	cr, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ChangeRequest{}, ErrNotFound
		}
		return ChangeRequest{}, err
	}
	return cr, nil
}

// List returns up to limit ChangeRequests for a tenant, newest first.
// status, when non-empty, filters to that single state.
func (s *Service) List(ctx context.Context, tenantID int64, status Status, limit int) ([]ChangeRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := selectColumns + ` FROM change_requests WHERE tenant_id=?`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, string(status))
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangeRequest
	for rows.Next() {
		cr, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// Submit moves a draft request into the review queue.
func (s *Service) Submit(ctx context.Context, tenantID, id, actorID int64) (ChangeRequest, error) {
	return s.transition(ctx, tenantID, id, actorID, StatusSubmitted, "")
}

// Approve marks the request approved by reviewer. The reviewer cannot
// be the original requester unless allowSelf is true (admins).
func (s *Service) Approve(ctx context.Context, tenantID, id, reviewerID int64, reason string, allowSelf bool) (ChangeRequest, error) {
	cr, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return ChangeRequest{}, err
	}
	if !allowSelf && cr.RequesterID == reviewerID {
		return ChangeRequest{}, ErrSelfApproval
	}
	return s.transitionFrom(ctx, cr, reviewerID, StatusApproved, reason, &reviewerID)
}

// Reject closes the request without applying it.
func (s *Service) Reject(ctx context.Context, tenantID, id, reviewerID int64, reason string) (ChangeRequest, error) {
	cr, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return ChangeRequest{}, err
	}
	return s.transitionFrom(ctx, cr, reviewerID, StatusRejected, reason, &reviewerID)
}

// Cancel allows the requester (or an admin) to abandon a non-terminal
// request without invoking the executor.
func (s *Service) Cancel(ctx context.Context, tenantID, id, actorID int64, reason string) (ChangeRequest, error) {
	return s.transition(ctx, tenantID, id, actorID, StatusCancelled, reason)
}

// MarkApplied is called by the executor after a successful Apply.
// `result` carries the executor's combined output, which is persisted
// to `change_requests.result`. The state-transition event records only
// a concise marker so the events table stays small even when the
// executor produces large output.
func (s *Service) MarkApplied(ctx context.Context, tenantID, id int64, result string) (ChangeRequest, error) {
	cr, err := s.transition(ctx, tenantID, id, 0, StatusApplied, "apply succeeded")
	if err != nil {
		return ChangeRequest{}, err
	}
	if result != "" {
		_, _ = s.DB.ExecContext(ctx,
			`UPDATE change_requests SET result=? WHERE tenant_id=? AND id=?`,
			result, tenantID, id)
		cr.Result = result
	}
	return cr, nil
}

// MarkFailed records a failed apply. The full reason is stored on the
// request row; the event log only carries a short marker (see
// MarkApplied for rationale).
func (s *Service) MarkFailed(ctx context.Context, tenantID, id int64, reason string) (ChangeRequest, error) {
	cr, err := s.transition(ctx, tenantID, id, 0, StatusFailed, "apply failed")
	if err != nil {
		return ChangeRequest{}, err
	}
	if reason != "" {
		_, _ = s.DB.ExecContext(ctx,
			`UPDATE change_requests SET result=? WHERE tenant_id=? AND id=?`,
			reason, tenantID, id)
		cr.Result = reason
	}
	return cr, nil
}

// Events returns the transition history for a request.
func (s *Service) Events(ctx context.Context, changeRequestID int64) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, change_request_id, actor_user_id, from_status, to_status, COALESCE(note,''), created_at
		 FROM change_request_events WHERE change_request_id=? ORDER BY id ASC`,
		changeRequestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e     Event
			actor sql.NullInt64
			from  sql.NullString
			ts    string
		)
		if err := rows.Scan(&e.ID, &e.ChangeRequestID, &actor, &from, &e.ToStatus, &e.Note, &ts); err != nil {
			return nil, err
		}
		if actor.Valid {
			v := actor.Int64
			e.ActorUserID = &v
		}
		if from.Valid {
			e.FromStatus = from.String
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CanApply reports whether the executor may run `cr`.
func CanApply(cr ChangeRequest) bool { return cr.Status == StatusApproved }

// ---- internals ----

func (s *Service) transition(ctx context.Context, tenantID, id, actorID int64, to Status, note string) (ChangeRequest, error) {
	cr, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return ChangeRequest{}, err
	}
	return s.transitionFrom(ctx, cr, actorID, to, note, nil)
}

func (s *Service) transitionFrom(ctx context.Context, cr ChangeRequest, actorID int64, to Status, note string, reviewerID *int64) (ChangeRequest, error) {
	if IsTerminal(cr.Status) {
		return ChangeRequest{}, ErrTerminal
	}
	if !canTransition(cr.Status, to) {
		return ChangeRequest{}, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, cr.Status, to)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	set := []string{"status=?", "updated_at=?"}
	args := []any{string(to), now}
	switch to {
	case StatusSubmitted:
		set = append(set, "submitted_at=?")
		args = append(args, now)
	case StatusApproved, StatusRejected:
		set = append(set, "decided_at=?")
		args = append(args, now)
		if note != "" {
			set = append(set, "decision_reason=?")
			args = append(args, note)
		}
		if reviewerID != nil {
			set = append(set, "reviewer_id=?")
			args = append(args, *reviewerID)
		}
	case StatusApplied, StatusFailed:
		set = append(set, "applied_at=?")
		args = append(args, now)
	}
	q := "UPDATE change_requests SET " + strings.Join(set, ", ") + " WHERE tenant_id=? AND id=? AND status=?"
	args = append(args, cr.TenantID, cr.ID, string(cr.Status))
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return ChangeRequest{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Lost an optimistic concurrency race against another transition.
		return ChangeRequest{}, ErrInvalidTransition
	}
	if err := s.recordEvent(ctx, cr.ID, actorID, string(cr.Status), string(to), note); err != nil {
		return ChangeRequest{}, err
	}
	return s.Get(ctx, cr.TenantID, cr.ID)
}

func (s *Service) recordEvent(ctx context.Context, crID, actorID int64, from, to, note string) error {
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	var fromV any
	if from != "" {
		fromV = from
	}
	var noteV any
	if note != "" {
		noteV = note
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO change_request_events(change_request_id, actor_user_id, from_status, to_status, note, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		crID, actor, fromV, to, noteV, time.Now().UTC().Format(time.RFC3339))
	return err
}

const selectColumns = `SELECT id, tenant_id, kind, title, COALESCE(description,''), requester_id,
	reviewer_id, status, COALESCE(decision_reason,''),
	push_job_id, COALESCE(variables,''), device_id,
	COALESCE(artifact,''), COALESCE(target_sha,''), COALESCE(payload,''), COALESCE(result,''),
	created_at, updated_at, submitted_at, decided_at, applied_at`

type scanner interface {
	Scan(...any) error
}

func scanRequest(s scanner) (ChangeRequest, error) {
	var (
		cr          ChangeRequest
		reviewer    sql.NullInt64
		pushJob     sql.NullInt64
		device      sql.NullInt64
		varsJSON    string
		createdAt   string
		updatedAt   string
		submittedAt sql.NullString
		decidedAt   sql.NullString
		appliedAt   sql.NullString
		kind        string
		status      string
	)
	if err := s.Scan(&cr.ID, &cr.TenantID, &kind, &cr.Title, &cr.Description, &cr.RequesterID,
		&reviewer, &status, &cr.DecisionReason,
		&pushJob, &varsJSON, &device,
		&cr.Artifact, &cr.TargetSHA, &cr.Payload, &cr.Result,
		&createdAt, &updatedAt, &submittedAt, &decidedAt, &appliedAt); err != nil {
		return ChangeRequest{}, err
	}
	cr.Kind = Kind(kind)
	cr.Status = Status(status)
	if reviewer.Valid {
		v := reviewer.Int64
		cr.ReviewerID = &v
	}
	if pushJob.Valid {
		v := pushJob.Int64
		cr.PushJobID = &v
	}
	if device.Valid {
		v := device.Int64
		cr.DeviceID = &v
	}
	if varsJSON != "" {
		_ = json.Unmarshal([]byte(varsJSON), &cr.Variables)
	}
	cr.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	cr.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if submittedAt.Valid {
		t, _ := time.Parse(time.RFC3339, submittedAt.String)
		cr.SubmittedAt = &t
	}
	if decidedAt.Valid {
		t, _ := time.Parse(time.RFC3339, decidedAt.String)
		cr.DecidedAt = &t
	}
	if appliedAt.Valid {
		t, _ := time.Parse(time.RFC3339, appliedAt.String)
		cr.AppliedAt = &t
	}
	return cr, nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableJSON(m map[string]string) any {
	if len(m) == 0 {
		return nil
	}
	b, _ := json.Marshal(m)
	return string(b)
}
