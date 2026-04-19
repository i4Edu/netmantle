package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/apitokens"
	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/changereq"
)

// ============================================================
// X-Request-ID propagation
// ============================================================

const headerRequestID = "X-Request-ID"

const (
	requestIDKey ctxKey = 2
	scopesKey    ctxKey = 3
)

// withRequestID assigns or accepts an X-Request-ID for every request,
// stashes it on the response header (so clients can correlate without
// reading the body), and on the context so audit emitters can record it.
func (s *server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := strings.TrimSpace(r.Header.Get(headerRequestID))
		if rid == "" || len(rid) > 128 {
			rid = newRequestID()
		}
		w.Header().Set(headerRequestID, rid)
		ctx := context.WithValue(r.Context(), requestIDKey, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// scopesFromContext returns the API-token scopes attached to a request
// (empty for cookie-authenticated sessions, where the user's role
// implicitly grants the corresponding capabilities).
func scopesFromContext(ctx context.Context) []string {
	v, _ := ctx.Value(scopesKey).([]string)
	return v
}

// ============================================================
// Bearer token authentication helper (used by authH in server.go).
// ============================================================

// userFromAPIToken loads the User row that owns a token so that the
// downstream RBAC middleware (requireWrite/requireAdmin) and tenant
// scoping continue to work without modification.
func (s *server) userFromAPIToken(ctx context.Context, t apitokens.Token) *auth.User {
	db, ok := s.DB.(*sql.DB)
	if !ok || db == nil {
		return nil
	}
	var (
		u    auth.User
		role string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT id, tenant_id, username, role FROM users WHERE id=?`,
		t.OwnerUserID,
	).Scan(&u.ID, &u.TenantID, &u.Username, &role); err != nil {
		return nil
	}
	u.Role = auth.Role(role)
	return &u
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// ============================================================
// API token endpoints
// ============================================================

type apiTokenInput struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

func (s *server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.APITokens.List(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []apitokens.Token{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in apiTokenInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var expPtr *time.Time
	if in.ExpiresAt != "" {
		t, err := parseRFC3339Loose(in.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at (want RFC3339)")
			return
		}
		expPtr = &t
	}
	tok, secret, err := s.APITokens.Issue(r.Context(), u.TenantID, u.ID, in.Name, in.Scopes, expPtr)
	if err != nil {
		if errors.Is(err, apitokens.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, "apitoken.create", fmt.Sprintf("apitoken:%d", tok.ID),
		"name="+tok.Name+" prefix="+tok.Prefix)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":   tok,
		"secret":  secret,
		"warning": "store this token now — it will not be shown again",
	})
}

func (s *server) handleDeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.APITokens.Revoke(r.Context(), u.TenantID, id); err != nil {
		if errors.Is(err, apitokens.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, "apitoken.revoke", fmt.Sprintf("apitoken:%d", id), "")
	w.WriteHeader(http.StatusNoContent)
}

// ============================================================
// Change request endpoints (Phase B)
// ============================================================

type changeRequestInput struct {
	Kind        string            `json:"kind"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	PushJobID   *int64            `json:"push_job_id,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
	DeviceID    *int64            `json:"device_id,omitempty"`
	Artifact    string            `json:"artifact,omitempty"`
	TargetSHA   string            `json:"target_sha,omitempty"`
}

type changeRequestDecisionInput struct {
	Reason string `json:"reason,omitempty"`
}

func (s *server) handleListChangeRequests(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	status := changereq.Status(r.URL.Query().Get("status"))
	out, err := s.ChangeReq.List(r.Context(), u.TenantID, status, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []changereq.ChangeRequest{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in changeRequestInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cr, err := s.ChangeReq.Create(r.Context(), changereq.ChangeRequest{
		TenantID: u.TenantID, Kind: changereq.Kind(in.Kind), Title: in.Title,
		Description: in.Description, RequesterID: u.ID,
		PushJobID: in.PushJobID, Variables: in.Variables,
		DeviceID: in.DeviceID, Artifact: in.Artifact, TargetSHA: in.TargetSHA,
	})
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	s.recordAudit(r, "changereq.create", fmt.Sprintf("changereq:%d", cr.ID),
		fmt.Sprintf("kind=%s title=%s", cr.Kind, cr.Title))
	writeJSON(w, http.StatusCreated, cr)
}

func (s *server) handleGetChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cr, err := s.ChangeReq.Get(r.Context(), u.TenantID, id)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	events, _ := s.ChangeReq.Events(r.Context(), cr.ID)
	writeJSON(w, http.StatusOK, map[string]any{"request": cr, "events": events})
}

func (s *server) handleSubmitChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cr, err := s.ChangeReq.Submit(r.Context(), u.TenantID, id, u.ID)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	s.recordAudit(r, "changereq.submit", fmt.Sprintf("changereq:%d", cr.ID), "")
	writeJSON(w, http.StatusOK, cr)
}

func (s *server) handleApproveChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in changeRequestDecisionInput
	_ = decodeJSON(r, &in)
	allowSelf := u.Role == auth.RoleAdmin
	cr, err := s.ChangeReq.Approve(r.Context(), u.TenantID, id, u.ID, in.Reason, allowSelf)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	s.recordAudit(r, "changereq.approve", fmt.Sprintf("changereq:%d", cr.ID), "reason="+in.Reason)
	writeJSON(w, http.StatusOK, cr)
}

func (s *server) handleRejectChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in changeRequestDecisionInput
	_ = decodeJSON(r, &in)
	cr, err := s.ChangeReq.Reject(r.Context(), u.TenantID, id, u.ID, in.Reason)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	s.recordAudit(r, "changereq.reject", fmt.Sprintf("changereq:%d", cr.ID), "reason="+in.Reason)
	writeJSON(w, http.StatusOK, cr)
}

func (s *server) handleCancelChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in changeRequestDecisionInput
	_ = decodeJSON(r, &in)
	cr, err := s.ChangeReq.Get(r.Context(), u.TenantID, id)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	if cr.RequesterID != u.ID && u.Role != auth.RoleAdmin {
		writeError(w, http.StatusForbidden, "only the requester or an admin may cancel")
		return
	}
	cr, err = s.ChangeReq.Cancel(r.Context(), u.TenantID, id, u.ID, in.Reason)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	s.recordAudit(r, "changereq.cancel", fmt.Sprintf("changereq:%d", cr.ID), "reason="+in.Reason)
	writeJSON(w, http.StatusOK, cr)
}

// handleApplyChangeRequest dispatches an approved request to the right
// executor (push automation or rollback) and records the result.
func (s *server) handleApplyChangeRequest(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cr, err := s.ChangeReq.Get(r.Context(), u.TenantID, id)
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	if !changereq.CanApply(cr) {
		writeError(w, http.StatusConflict, "request is not in 'approved' state")
		return
	}
	out, applyErr := s.applyChangeRequest(r, cr)
	if applyErr != nil {
		_, _ = s.ChangeReq.MarkFailed(r.Context(), u.TenantID, id, applyErr.Error())
		s.recordAudit(r, "changereq.apply", fmt.Sprintf("changereq:%d", cr.ID),
			"status=failed err="+applyErr.Error())
		writeError(w, http.StatusBadGateway, applyErr.Error())
		return
	}
	cr, err = s.ChangeReq.MarkApplied(r.Context(), u.TenantID, id, out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, "changereq.apply", fmt.Sprintf("changereq:%d", cr.ID), "status=applied")
	writeJSON(w, http.StatusOK, cr)
}

// applyChangeRequest performs the side-effecting work for a request
// that has cleared the approval queue. It returns the executor output
// or an error to be surfaced to the caller.
func (s *server) applyChangeRequest(r *http.Request, cr changereq.ChangeRequest) (string, error) {
	switch cr.Kind {
	case changereq.KindPush:
		if s.Automation == nil || cr.PushJobID == nil {
			return "", errors.New("push automation not available")
		}
		results, err := s.Automation.Run(r.Context(), cr.TenantID, *cr.PushJobID, 4)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, res := range results {
			fmt.Fprintf(&b, "[%s] %s\n", res.Status, res.Hostname)
			if res.Error != "" {
				fmt.Fprintf(&b, "  error: %s\n", res.Error)
			}
		}
		return b.String(), nil
	case changereq.KindRollback:
		if cr.DeviceID == nil {
			return "", errors.New("rollback request has no device id")
		}
		dev, err := s.Devices.GetDevice(r.Context(), cr.TenantID, *cr.DeviceID)
		if err != nil {
			return "", fmt.Errorf("device: %w", err)
		}
		// Reuse the automation Executor closure if available — it knows
		// how to drive a driver session through the SSH transport. The
		// rollback payload was captured at request-creation time so the
		// Senior approving the request saw the exact bytes that will
		// be applied.
		if s.Automation == nil || s.Automation.Executor == nil {
			return "", errors.New("no executor configured")
		}
		return s.Automation.Executor(r.Context(), dev, cr.Payload)
	}
	return "", fmt.Errorf("unknown kind %q", cr.Kind)
}

func (s *server) changeReqError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, changereq.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, changereq.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, changereq.ErrInvalidTransition),
		errors.Is(err, changereq.ErrTerminal):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, changereq.ErrSelfApproval):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// ============================================================
// Rollback endpoint (Phase D)
// ============================================================

type rollbackInput struct {
	Artifact  string `json:"artifact"`
	TargetSHA string `json:"target_sha"`
	Reason    string `json:"reason,omitempty"`
	Immediate bool   `json:"immediate,omitempty"`
}

// handleRollbackDevice creates a rollback ChangeRequest carrying the
// historical config bytes. By default it leaves the request in `draft`
// state; the requester must Submit and a Senior must Approve before
// the executor will apply it. Admins may pass `immediate=true` to
// short-circuit the queue (the request is still created so the action
// remains auditable, but it transitions through submit/approve/apply
// in a single API call).
func (s *server) handleRollbackDevice(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	devID, ok := pathID(w, r)
	if !ok {
		return
	}
	var in rollbackInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Artifact == "" || in.TargetSHA == "" {
		writeError(w, http.StatusBadRequest, "artifact and target_sha required")
		return
	}
	if in.Immediate && u.Role != auth.RoleAdmin {
		writeError(w, http.StatusForbidden, "immediate rollback requires admin role")
		return
	}
	dev, err := s.Devices.GetDevice(r.Context(), u.TenantID, devID)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	body, err := s.Backup.ReadVersion(r.Context(), u.TenantID, devID, in.Artifact, in.TargetSHA)
	if err != nil {
		writeError(w, http.StatusNotFound, "historical config not found: "+err.Error())
		return
	}
	devIDCopy := devID
	cr, err := s.ChangeReq.Create(r.Context(), changereq.ChangeRequest{
		TenantID: u.TenantID, Kind: changereq.KindRollback,
		Title:       fmt.Sprintf("rollback %s/%s to %s", dev.Hostname, in.Artifact, short(in.TargetSHA)),
		Description: in.Reason, RequesterID: u.ID,
		DeviceID: &devIDCopy, Artifact: in.Artifact, TargetSHA: in.TargetSHA,
		Payload: string(body),
	})
	if err != nil {
		s.changeReqError(w, err)
		return
	}
	s.recordAudit(r, "rollback.request", fmt.Sprintf("device:%d", devID),
		fmt.Sprintf("changereq=%d artifact=%s sha=%s", cr.ID, in.Artifact, in.TargetSHA))
	if !in.Immediate {
		writeJSON(w, http.StatusCreated, cr)
		return
	}
	if cr, err = s.ChangeReq.Submit(r.Context(), u.TenantID, cr.ID, u.ID); err != nil {
		s.changeReqError(w, err)
		return
	}
	if cr, err = s.ChangeReq.Approve(r.Context(), u.TenantID, cr.ID, u.ID,
		"emergency rollback by admin", true); err != nil {
		s.changeReqError(w, err)
		return
	}
	out, applyErr := s.applyChangeRequest(r, cr)
	if applyErr != nil {
		_, _ = s.ChangeReq.MarkFailed(r.Context(), u.TenantID, cr.ID, applyErr.Error())
		s.recordAudit(r, "rollback.apply", fmt.Sprintf("changereq:%d", cr.ID),
			"status=failed err="+applyErr.Error())
		writeError(w, http.StatusBadGateway, applyErr.Error())
		return
	}
	cr, _ = s.ChangeReq.MarkApplied(r.Context(), u.TenantID, cr.ID, out)
	s.recordAudit(r, "rollback.apply", fmt.Sprintf("changereq:%d", cr.ID), "status=applied")
	writeJSON(w, http.StatusOK, cr)
}

func short(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// ============================================================
// Push-job override (Phase B): direct Run requires admin (or the
// apply:direct API-token scope), otherwise callers must apply via an
// approved change-request.
// ============================================================

func (s *server) handleRunPushGuarded(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	scopes := scopesFromContext(r.Context())
	allowed := u != nil && (u.Role == auth.RoleAdmin || apitokens.HasScope(scopes, "apply:direct"))
	if !allowed {
		writeError(w, http.StatusConflict,
			"direct push requires admin role or apply:direct scope; create an approved ChangeRequest and apply it instead")
		return
	}
	s.handleRunPush(w, r)
}
