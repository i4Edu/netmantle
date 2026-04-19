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
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
	"github.com/i4Edu/netmantle/internal/observability"
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

	// Embedded UI (single-page).
	mux.Handle("GET /", web.Handler())

	// Wrap with logging + metrics.
	return s.recover(s.logRequests(s.metrics(mux)))
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		h(w, r.WithContext(ctx))
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
		// Backup may have created a "failed" run row before returning.
		if run != nil {
			writeJSON(w, http.StatusBadGateway, run)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
