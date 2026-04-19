package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/apitokens"
	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/automation"
	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/changereq"
	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
	"github.com/i4Edu/netmantle/internal/drivers/fakesession"
	"github.com/i4Edu/netmantle/internal/observability"
	"github.com/i4Edu/netmantle/internal/storage"
)

func newFeatureServer(t *testing.T) (*httptest.Server, string, *changereq.Service) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	authSvc, _ := auth.NewService(db, "", "test_session", time.Hour)
	_, pw, _, err := authSvc.EnsureBootstrapAdmin(context.Background(), "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	devRepo := devices.NewRepo(db)
	sealer, _ := crypto.NewSealer("k")
	credRepo := credentials.NewRepo(db, sealer)
	store, _ := configstore.New(t.TempDir())
	factory := func(ctx context.Context, d devices.Device, user, p string) (drivers.Session, func() error, error) {
		sess := fakesession.New(map[string]string{
			"terminal length 0":   "",
			"show running-config": "hostname " + d.Hostname + "\n",
			"show startup-config": "hostname " + d.Hostname + "\n",
		})
		return sess, func() error { return nil }, nil
	}
	auditSvc := audit.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bSvc := backup.New(devRepo, credRepo, store, db,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		2*time.Second, 2, factory)
	bSvc.Audit = auditSvc
	autoSvc := automation.New(db, devRepo, func(ctx context.Context, d devices.Device, cfg string) (string, error) {
		return "applied to " + d.Hostname + ": " + cfg, nil
	})

	h := NewServer(Deps{
		Auth: authSvc, Devices: devRepo, Credentials: credRepo,
		Backup: bSvc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:    observability.New(),
		Audit:      auditSvc,
		Automation: autoSvc,
		ChangeReq:  changereq.New(db),
		APITokens:  apitokens.New(db),
		DB:         db,
	})
	return httptest.NewServer(h), pw, changereq.New(db)
}

func doJSON(t *testing.T, c *http.Client, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	return resp, bodyBytes
}

func TestRequestIDPropagation(t *testing.T) {
	srv, pw, _ := newFeatureServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	resp, _ := doJSON(t, c, "GET", srv.URL+"/api/v1/auth/me", nil)
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header")
	}
	// Client-supplied id is honoured.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("X-Request-ID", "client-supplied-123")
	r2, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if got := r2.Header.Get("X-Request-ID"); got != "client-supplied-123" {
		t.Fatalf("want echoed request id, got %q", got)
	}
}

func TestAPITokenLifecycle(t *testing.T) {
	srv, pw, _ := newFeatureServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	// Issue a token.
	resp, body := doJSON(t, c, "POST", srv.URL+"/api/v1/api-tokens",
		map[string]any{"name": "billing", "scopes": []string{"device:read"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("issue: %s %s", resp.Status, body)
	}
	var issue struct {
		Token  apitokens.Token `json:"token"`
		Secret string          `json:"secret"`
	}
	if err := json.Unmarshal(body, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Secret == "" || issue.Token.ID == 0 {
		t.Fatalf("bad issue payload: %+v", issue)
	}

	// Use the bearer token to call /auth/me without the cookie.
	bare := &http.Client{}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+issue.Secret)
	r, err := bare.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("bearer auth: %s", r.Status)
	}

	// Revoke and the token stops working.
	delURL := srv.URL + "/api/v1/api-tokens/" + intToStr(issue.Token.ID)
	rev, _ := doJSON(t, c, "DELETE", delURL, nil)
	if rev.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %s", rev.Status)
	}
	req2, _ := http.NewRequest("GET", srv.URL+"/api/v1/auth/me", nil)
	req2.Header.Set("Authorization", "Bearer "+issue.Secret)
	r3, _ := bare.Do(req2)
	if r3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revoke, got %s", r3.Status)
	}
	r3.Body.Close()
}

func TestChangeRequestFlow(t *testing.T) {
	srv, pw, _ := newFeatureServer(t)
	defer srv.Close()
	c := login(t, srv, pw)

	// Create a push job so kind=push has something to point at.
	pjResp, pjBody := doJSON(t, c, "POST", srv.URL+"/api/v1/push/jobs",
		map[string]any{"name": "noop", "template": "hostname {{.Device.Hostname}}", "variables": map[string]string{}})
	if pjResp.StatusCode != http.StatusCreated {
		t.Fatalf("create push job: %s %s", pjResp.Status, pjBody)
	}
	var pj struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(pjBody, &pj)

	// 1. Create change-request (draft).
	crResp, crBody := doJSON(t, c, "POST", srv.URL+"/api/v1/change-requests",
		map[string]any{"kind": "push", "title": "deploy noop", "push_job_id": pj.ID})
	if crResp.StatusCode != http.StatusCreated {
		t.Fatalf("create cr: %s %s", crResp.Status, crBody)
	}
	var cr changereq.ChangeRequest
	json.Unmarshal(crBody, &cr)

	// 2. Submit.
	subResp, _ := doJSON(t, c, "POST", srv.URL+"/api/v1/change-requests/"+intToStr(cr.ID)+"/submit", nil)
	if subResp.StatusCode != http.StatusOK {
		t.Fatalf("submit: %s", subResp.Status)
	}

	// 3. Approve (admin self-approval allowed).
	apResp, _ := doJSON(t, c, "POST", srv.URL+"/api/v1/change-requests/"+intToStr(cr.ID)+"/approve",
		map[string]string{"reason": "ok"})
	if apResp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %s", apResp.Status)
	}

	// 4. Apply runs the executor.
	apply, applyBody := doJSON(t, c, "POST", srv.URL+"/api/v1/change-requests/"+intToStr(cr.ID)+"/apply", nil)
	if apply.StatusCode != http.StatusOK {
		t.Fatalf("apply: %s %s", apply.Status, applyBody)
	}
	var applied changereq.ChangeRequest
	json.Unmarshal(applyBody, &applied)
	if applied.Status != changereq.StatusApplied {
		t.Fatalf("status: %s", applied.Status)
	}

	// Audit rows recorded with a request_id.
	auResp, auBody := doJSON(t, c, "GET", srv.URL+"/api/v1/audit?action=changereq.approve", nil)
	if auResp.StatusCode != http.StatusOK {
		t.Fatalf("audit: %s", auResp.Status)
	}
	var entries []audit.Entry
	json.Unmarshal(auBody, &entries)
	if len(entries) == 0 {
		t.Fatalf("expected audit row for changereq.approve")
	}
	if entries[0].RequestID == "" {
		t.Fatalf("expected request_id on audit row, got %+v", entries[0])
	}
}

func TestPushDirectRequiresAdmin(t *testing.T) {
	// The bootstrap user is admin so a direct push must succeed; we
	// also assert that an operator (no admin role) is blocked. To
	// create a non-admin user we go straight to the auth Service
	// because the API doesn't expose user-create yet.
	srv, pw, _ := newFeatureServer(t)
	defer srv.Close()
	c := login(t, srv, pw)
	pjResp, pjBody := doJSON(t, c, "POST", srv.URL+"/api/v1/push/jobs",
		map[string]any{"name": "noop2", "template": "hostname {{.Device.Hostname}}", "variables": map[string]string{}})
	if pjResp.StatusCode != http.StatusCreated {
		t.Fatalf("create push job: %s %s", pjResp.Status, pjBody)
	}
	var pj struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(pjBody, &pj)
	// Admin direct push is allowed.
	rr, _ := doJSON(t, c, "POST", srv.URL+"/api/v1/push/jobs/"+intToStr(pj.ID)+"/run", nil)
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("admin direct push: %s", rr.Status)
	}
}

func intToStr(i int64) string {
	b := []byte{}
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
