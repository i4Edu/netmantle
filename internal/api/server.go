// Package api implements the HTTP/JSON API surface and serves the embedded
// OpenAPI spec, Swagger UI, and the static web UI.
//
// The router is plain net/http; we add a tiny pattern matcher rather than
// importing a third-party router so the binary stays small.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/apitokens"
	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/automation"
	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/changereq"
	"github.com/i4Edu/netmantle/internal/changes"
	"github.com/i4Edu/netmantle/internal/compliance"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/discovery"
	"github.com/i4Edu/netmantle/internal/drivers"
	"github.com/i4Edu/netmantle/internal/gitops"
	"github.com/i4Edu/netmantle/internal/notify"
	"github.com/i4Edu/netmantle/internal/observability"
	"github.com/i4Edu/netmantle/internal/poller"
	"github.com/i4Edu/netmantle/internal/probes"
	"github.com/i4Edu/netmantle/internal/search"
	"github.com/i4Edu/netmantle/internal/tenants"
	"github.com/i4Edu/netmantle/internal/terminal"
	"github.com/i4Edu/netmantle/internal/version"
	"github.com/i4Edu/netmantle/internal/web"
)

//go:embed openapi/openapi.yaml
var openapiFS embed.FS

// Deps bundles the collaborators a Server needs.
type Deps struct {
	Auth        *auth.Service
	Devices     *devices.Repo
	Credentials *credentials.Repo
	Backup      *backup.Service
	Logger      *slog.Logger
	Metrics     *observability.Metrics
	Audit       *audit.Service

	// Optional Phase 2..10 services. Endpoints registered for each are only
	// installed when the corresponding pointer is non-nil.
	Changes    *changes.Service
	Notify     *notify.Service
	Search     *search.Service
	Compliance *compliance.Service
	Discovery  *discovery.Service
	Automation *automation.Service
	Probes     *probes.Service
	Tenants    *tenants.Service
	Pollers    *poller.Service
	Terminal   *terminal.Service
	GitOps     *gitops.Service

	// Phase A..E feature services. Each is optional so existing
	// integration tests that only wire the minimum continue to work.
	ChangeReq *changereq.Service
	APITokens *apitokens.Service

	DB any // *sql.DB; opaque here to avoid an import cycle
}

// NewServer returns a configured *http.ServeMux. The caller wraps it in a
// *http.Server with the desired timeouts.
func NewServer(d Deps) http.Handler {
	mux := http.NewServeMux()
	s := &server{Deps: d}

	// Public.
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/openapi.yaml", s.handleOpenAPI)
	mux.HandleFunc("GET /api/docs", s.handleDocs)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	// Authenticated.
	mux.Handle("POST /api/v1/auth/logout", s.auth(s.handleLogout))
	mux.Handle("GET /api/v1/auth/me", s.auth(s.handleMe))

	mux.Handle("GET /api/v1/drivers", s.auth(s.handleListDrivers))

	mux.Handle("GET /api/v1/devices", s.auth(s.handleListDevices))
	mux.Handle("POST /api/v1/devices", s.auth(s.requireWrite(s.handleCreateDevice)))
	mux.Handle("GET /api/v1/devices/{id}", s.auth(s.handleGetDevice))
	mux.Handle("PUT /api/v1/devices/{id}", s.auth(s.requireWrite(s.handleUpdateDevice)))
	mux.Handle("DELETE /api/v1/devices/{id}", s.auth(s.requireWrite(s.handleDeleteDevice)))
	mux.Handle("POST /api/v1/devices/{id}/backup", s.auth(s.requireWrite(s.handleBackupNow)))
	mux.Handle("GET /api/v1/devices/{id}/runs", s.auth(s.handleListRuns))
	mux.Handle("GET /api/v1/devices/{id}/config", s.auth(s.handleGetConfig))

	mux.Handle("GET /api/v1/device-groups", s.auth(s.handleListGroups))
	mux.Handle("POST /api/v1/device-groups", s.auth(s.requireWrite(s.handleCreateGroup)))

	mux.Handle("GET /api/v1/credentials", s.auth(s.handleListCredentials))
	mux.Handle("POST /api/v1/credentials", s.auth(s.requireWrite(s.handleCreateCredential)))
	mux.Handle("DELETE /api/v1/credentials/{id}", s.auth(s.requireWrite(s.handleDeleteCredential)))

	// Audit log (read-only). Available to any authenticated user; results
	// are tenant-scoped.
	mux.Handle("GET /api/v1/audit", s.auth(s.handleListAudit))

	// Dashboard summary aggregation (read-only, tenant-scoped).
	mux.Handle("GET /api/v1/dashboard/summary", s.auth(s.handleDashboardSummary))

	// Phase 2 — changes & notifications.
	if d.Changes != nil {
		mux.Handle("GET /api/v1/changes", s.auth(s.handleListChanges))
		mux.Handle("GET /api/v1/changes/{id}/diff", s.auth(s.handleChangeDiff))
		mux.Handle("POST /api/v1/changes/{id}/review", s.auth(s.requireWrite(s.handleMarkReviewed)))
	}
	if d.Notify != nil {
		mux.Handle("GET /api/v1/notifications/channels", s.auth(s.handleListChannels))
		mux.Handle("POST /api/v1/notifications/channels", s.auth(s.requireWrite(s.handleCreateChannel)))
		mux.Handle("DELETE /api/v1/notifications/channels/{id}", s.auth(s.requireWrite(s.handleDeleteChannel)))
		mux.Handle("GET /api/v1/notifications/rules", s.auth(s.handleListRules))
		mux.Handle("POST /api/v1/notifications/rules", s.auth(s.requireWrite(s.handleCreateRule)))
	}

	// Phase 3 — search & saved searches.
	if d.Search != nil {
		mux.Handle("GET /api/v1/search", s.auth(s.handleSearch))
		mux.Handle("GET /api/v1/search/saved", s.auth(s.handleListSaved))
		mux.Handle("POST /api/v1/search/saved", s.auth(s.requireWrite(s.handleSaveSearch)))
		mux.Handle("GET /api/v1/changes.csv", s.auth(s.handleChangesCSV))
	}

	// Phase 4 — compliance.
	if d.Compliance != nil {
		mux.Handle("GET /api/v1/compliance/rules", s.auth(s.handleListComplianceRules))
		mux.Handle("POST /api/v1/compliance/rules", s.auth(s.requireWrite(s.handleCreateComplianceRule)))
		mux.Handle("DELETE /api/v1/compliance/rules/{id}", s.auth(s.requireWrite(s.handleDeleteComplianceRule)))
		mux.Handle("GET /api/v1/compliance/findings", s.auth(s.handleListFindings))
		// Rule packs: catalogue listing is read-only; applying a pack is operator+.
		mux.Handle("GET /api/v1/compliance/rulepacks", s.auth(s.handleListRulePacks))
		mux.Handle("POST /api/v1/compliance/rulepacks/{name}/apply", s.auth(s.requireWrite(s.handleApplyRulePack)))
		mux.Handle("GET /api/v1/compliance/rulepack-assignments", s.auth(s.handleListGroupRulePackAssignments))
		mux.Handle("PUT /api/v1/compliance/rulepack-assignments/{id}", s.auth(s.requireWrite(s.handleSetGroupRulePackAssignments)))
	}

	// Phase 5 — discovery.
	if d.Discovery != nil {
		mux.Handle("POST /api/v1/discovery/scans", s.auth(s.requireWrite(s.handleStartScan)))
		mux.Handle("POST /api/v1/discovery/import/netbox", s.auth(s.requireWrite(s.handleImportNetBox)))
	}

	// Phase 6 — push automation.
	if d.Automation != nil {
		mux.Handle("GET /api/v1/push/jobs", s.auth(s.handleListPushJobs))
		mux.Handle("POST /api/v1/push/jobs", s.auth(s.requireWrite(s.handleCreatePushJob)))
		mux.Handle("POST /api/v1/push/jobs/{id}/preview", s.auth(s.handlePreviewPush))
		// Direct push requires admin role (or the apply:direct API-token
		// scope). Other callers must go through the change-request flow.
		mux.Handle("POST /api/v1/push/jobs/{id}/run", s.auth(s.requireWrite(s.handleRunPushGuarded)))
	}

	// Phase 7 — pollers + in-app CLI.
	if d.Pollers != nil {
		mux.Handle("GET /api/v1/pollers", s.auth(s.handleListPollers))
		mux.Handle("POST /api/v1/pollers", s.auth(s.requireWrite(s.handleRegisterPoller)))
		mux.Handle("DELETE /api/v1/pollers/{id}", s.auth(s.requireWrite(s.handleDeletePoller)))
	}
	if d.Terminal != nil {
		// WS upgrade: GET /api/v1/devices/{id}/terminal
		mux.Handle("GET /api/v1/devices/{id}/terminal", s.authH(s.handleTerminal()))
	}

	// Phase 8 — probes & runtime compliance.
	if d.Probes != nil {
		mux.Handle("GET /api/v1/probes", s.auth(s.handleListProbes))
		mux.Handle("POST /api/v1/probes", s.auth(s.requireWrite(s.handleCreateProbe)))
		mux.Handle("DELETE /api/v1/probes/{id}", s.auth(s.requireWrite(s.handleDeleteProbe)))
		mux.Handle("GET /api/v1/probes/{id}/runs", s.auth(s.handleListProbeRuns))
	}

	// Phase 9 — tenants & quotas.
	if d.Tenants != nil {
		mux.Handle("GET /api/v1/tenants", s.auth(s.requireAdmin(s.handleListTenants)))
		mux.Handle("POST /api/v1/tenants", s.auth(s.requireAdmin(s.handleCreateTenant)))
		mux.Handle("PUT /api/v1/tenants/{id}/quota", s.auth(s.requireAdmin(s.handleSetTenantQuota)))
	}

	// Phase 10 — topology & GitOps mirror.
	mux.Handle("GET /api/v1/topology", s.auth(s.handleTopology))
	if d.GitOps != nil {
		mux.Handle("GET /api/v1/gitops/mirror", s.auth(s.handleGetMirror))
		mux.Handle("PUT /api/v1/gitops/mirror", s.auth(s.requireAdmin(s.handlePutMirror)))
	}

	// Approval workflow + rollback (Phases B & D).
	if d.ChangeReq != nil {
		mux.Handle("GET /api/v1/change-requests", s.auth(s.handleListChangeRequests))
		mux.Handle("POST /api/v1/change-requests", s.auth(s.requireWrite(s.handleCreateChangeRequest)))
		mux.Handle("GET /api/v1/change-requests/{id}", s.auth(s.handleGetChangeRequest))
		mux.Handle("POST /api/v1/change-requests/{id}/submit", s.auth(s.requireWrite(s.handleSubmitChangeRequest)))
		mux.Handle("POST /api/v1/change-requests/{id}/approve", s.auth(s.requireAdmin(s.handleApproveChangeRequest)))
		mux.Handle("POST /api/v1/change-requests/{id}/reject", s.auth(s.requireAdmin(s.handleRejectChangeRequest)))
		mux.Handle("POST /api/v1/change-requests/{id}/cancel", s.auth(s.requireWrite(s.handleCancelChangeRequest)))
		mux.Handle("POST /api/v1/change-requests/{id}/apply", s.auth(s.requireWrite(s.handleApplyChangeRequest)))
		if d.Backup != nil {
			mux.Handle("POST /api/v1/devices/{id}/rollback", s.auth(s.requireWrite(s.handleRollbackDevice)))
		}
	}

	// API tokens for machine-to-machine integrations (Phase E).
	// Issuance and revocation are admin-only: a token may carry the
	// `apply:direct` scope (which lets handleRunPushGuarded bypass the
	// approval workflow), so allowing operators to mint tokens would
	// let them grant themselves direct-apply privileges.
	if d.APITokens != nil {
		mux.Handle("GET /api/v1/api-tokens", s.auth(s.handleListAPITokens))
		mux.Handle("POST /api/v1/api-tokens", s.auth(s.requireAdmin(s.handleCreateAPIToken)))
		mux.Handle("DELETE /api/v1/api-tokens/{id}", s.auth(s.requireAdmin(s.handleDeleteAPIToken)))
	}

	// Embedded UI (single-page).
	mux.Handle("GET /", web.Handler())

	// Wrap with request-id propagation (innermost) + logging + metrics.
	return s.recover(s.logRequests(s.metrics(s.withRequestID(mux))))
}

type server struct {
	Deps
}

// ---------------- middlewares ----------------

func (s *server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Logger.Error("panic in handler", "err", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		s.Logger.Info("http",
			"method", r.Method, "path", r.URL.Path,
			"status", rw.status, "dur_ms", time.Since(start).Milliseconds())
	})
}

func (s *server) metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		s.Metrics.ObserveHTTP(r.Method, rw.status, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

type ctxKey int

const userKey ctxKey = 1

func userFromContext(ctx context.Context) *auth.User {
	v, _ := ctx.Value(userKey).(*auth.User)
	return v
}

func (s *server) auth(h http.HandlerFunc) http.Handler {
	return s.authH(http.HandlerFunc(h))
}

// authH wraps any http.Handler with session authentication. As of the
// API-tokens phase, this also accepts an `Authorization: Bearer …`
// header issued via /api/v1/api-tokens; cookie auth keeps working
// unchanged for the embedded UI.
func (s *server) authH(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Bearer token (if APITokens is configured and a header is present).
		if tok := bearerFromHeader(r); tok != "" && s.APITokens != nil {
			t, err := s.APITokens.Authenticate(r.Context(), tok)
			if err == nil {
				if u := s.userFromAPIToken(r.Context(), t); u != nil {
					ctx := context.WithValue(r.Context(), userKey, u)
					ctx = context.WithValue(ctx, scopesKey, t.Scopes)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			// Fall through to cookie auth on failure (e.g. revoked
			// token alongside a still-valid session).
		}
		// 2. Cookie session.
		c, err := r.Cookie(s.Auth.CookieName())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		u, err := s.Auth.LookupSession(r.Context(), c.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) requireWrite(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromContext(r.Context())
		if u == nil || !u.Role.CanWrite() {
			writeError(w, http.StatusForbidden, "insufficient role")
			return
		}
		h(w, r)
	}
}

func (s *server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromContext(r.Context())
		if u == nil || u.Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		h(w, r)
	}
}

// recordAudit writes a row to audit_log if the audit service is configured.
// The source defaults to "api"; callers using a different channel (e.g. UI
// vs. a future webhook) may pass an explicit value through audit.Record.
// The per-request correlation id (X-Request-ID) is propagated automatically
// so all audit rows produced by a single HTTP call share a request_id.
func (s *server) recordAudit(r *http.Request, action, target, detail string) {
	if s.Audit == nil {
		return
	}
	u := userFromContext(r.Context())
	var tenantID, actorID int64
	if u != nil {
		tenantID = u.TenantID
		actorID = u.ID
	}
	s.Audit.RecordWithRequest(r.Context(), tenantID, actorID,
		requestIDFrom(r.Context()), audit.SourceAPI, action, target, detail)
}

// ---------------- handlers ----------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
	})
}

func (s *server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	b, err := openapiFS.ReadFile("openapi/openapi.yaml")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "spec missing")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(b)
}

func (s *server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, swaggerHTML)
}

const swaggerHTML = `<!doctype html>
<html><head><title>NetMantle API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head><body>
<div id="swagger"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>
window.ui = SwaggerUIBundle({url:'/api/openapi.yaml', dom_id:'#swagger'});
</script>
</body></html>`

// auth

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	u, err := s.Auth.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	cookie, exp, err := s.Auth.CreateSession(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.Auth.CookieName(),
		Value:    cookie,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, u)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.Auth.CookieName()); err == nil {
		_ = s.Auth.DestroySession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.Auth.CookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFromContext(r.Context()))
}

// drivers

func (s *server) handleListDrivers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, drivers.List())
}

// devices

type deviceInput struct {
	Hostname     string `json:"hostname"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	Driver       string `json:"driver"`
	GroupID      *int64 `json:"group_id"`
	CredentialID *int64 `json:"credential_id"`
}

func (s *server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Devices.ListDevices(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []devices.Device{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in deviceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	d, err := s.Devices.CreateDevice(r.Context(), devices.Device{
		TenantID: u.TenantID, Hostname: in.Hostname, Address: in.Address, Port: in.Port,
		Driver: in.Driver, GroupID: in.GroupID, CredentialID: in.CredentialID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, "device.create", fmt.Sprintf("device:%d", d.ID),
		fmt.Sprintf("hostname=%s driver=%s", d.Hostname, d.Driver))
	writeJSON(w, http.StatusCreated, d)
}

func (s *server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Devices.GetDevice(r.Context(), u.TenantID, id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in deviceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	d, err := s.Devices.UpdateDevice(r.Context(), devices.Device{
		ID: id, TenantID: u.TenantID, Hostname: in.Hostname, Address: in.Address, Port: in.Port,
		Driver: in.Driver, GroupID: in.GroupID, CredentialID: in.CredentialID,
	})
	if err != nil {
		notFoundOr400(w, err)
		return
	}
	s.recordAudit(r, "device.update", fmt.Sprintf("device:%d", d.ID),
		fmt.Sprintf("hostname=%s driver=%s", d.Hostname, d.Driver))
	writeJSON(w, http.StatusOK, d)
}

func (s *server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Devices.DeleteDevice(r.Context(), u.TenantID, id); err != nil {
		notFoundOr500(w, err)
		return
	}
	s.recordAudit(r, "device.delete", fmt.Sprintf("device:%d", id), "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	run, err := s.Backup.BackupNow(r.Context(), u.TenantID, id, u.Username)
	if err != nil {
		s.recordAudit(r, "device.backup.request",
			fmt.Sprintf("device:%d", id), "status=failed err="+err.Error())
		// Backup may have created a "failed" run row before returning.
		if run != nil {
			writeJSON(w, http.StatusBadGateway, run)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit(r, "device.backup.request",
		fmt.Sprintf("device:%d", id), fmt.Sprintf("run_id=%d status=%s", run.ID, run.Status))
	writeJSON(w, http.StatusOK, run)
}

func (s *server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	runs, err := s.Backup.ListRuns(r.Context(), id, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []backup.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	artifact := r.URL.Query().Get("artifact")
	body, sha, err := s.Backup.LatestVersion(r.Context(), u.TenantID, id, artifact)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Commit-SHA", sha)
	_, _ = w.Write(body)
}

// groups

type groupInput struct {
	Name string `json:"name"`
}

func (s *server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	gs, err := s.Devices.ListGroups(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if gs == nil {
		gs = []devices.Group{}
	}
	writeJSON(w, http.StatusOK, gs)
}

func (s *server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in groupInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	g, err := s.Devices.CreateGroup(r.Context(), devices.Group{TenantID: u.TenantID, Name: in.Name})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, "device_group.create", fmt.Sprintf("group:%d", g.ID), "name="+g.Name)
	writeJSON(w, http.StatusCreated, g)
}

// credentials

type credentialInput struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

func (s *server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	out, err := s.Credentials.List(r.Context(), u.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []credentials.Credential{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	var in credentialInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.Credentials.Create(r.Context(),
		credentials.Credential{TenantID: u.TenantID, Name: in.Name, Username: in.Username},
		in.Secret)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// NOTE: never log the secret; only the name + username + id.
	s.recordAudit(r, "credential.create", fmt.Sprintf("credential:%d", c.ID),
		fmt.Sprintf("name=%s username=%s", c.Name, c.Username))
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	u := userFromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Credentials.Delete(r.Context(), u.TenantID, id); err != nil {
		notFoundOr500(w, err)
		return
	}
	s.recordAudit(r, "credential.delete", fmt.Sprintf("credential:%d", id), "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------- helpers ----------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		// Manual fallback: last segment.
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		if len(parts) > 0 {
			raw = parts[len(parts)-1]
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, devices.ErrNotFound) || errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func notFoundOr400(w http.ResponseWriter, err error) {
	if errors.Is(err, devices.ErrNotFound) || errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}
