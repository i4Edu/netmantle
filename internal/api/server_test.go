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

	"github.com/i4Edu/netmantle/internal/audit"
	"github.com/i4Edu/netmantle/internal/auth"
	"github.com/i4Edu/netmantle/internal/backup"
	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/credentials"
	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/devices"
	"github.com/i4Edu/netmantle/internal/drivers"
	_ "github.com/i4Edu/netmantle/internal/drivers/builtin"
	"github.com/i4Edu/netmantle/internal/drivers/fakesession"
	"github.com/i4Edu/netmantle/internal/observability"
	"github.com/i4Edu/netmantle/internal/storage"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
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
	bSvc := backup.New(devRepo, credRepo, store, db,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		2*time.Second, 2, factory)

	h := NewServer(Deps{
		Auth: authSvc, Devices: devRepo, Credentials: credRepo,
		Backup: bSvc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: observability.New(),
		Audit:   audit.New(db, slog.New(slog.NewTextHandler(io.Discard, nil))),
	})
	return httptest.NewServer(h), pw
}

func login(t *testing.T, srv *httptest.Server, pw string) *http.Client {
	t.Helper()
	jar := newCookieJar(t, srv.URL)
	c := &http.Client{Jar: jar}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": pw})
	resp, err := c.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %s", resp.Status)
	}
	return c
}

func newCookieJar(t *testing.T, _ string) http.CookieJar {
	t.Helper()
	jar, err := cookieJarFunc()
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

func TestAPIEndToEnd(t *testing.T) {
	srv, pw := newTestServer(t)
	defer srv.Close()

	// Unauthenticated request rejected.
	resp, _ := http.Get(srv.URL + "/api/v1/devices")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %s", resp.Status)
	}

	c := login(t, srv, pw)

	// Drivers listed.
	resp, err := c.Get(srv.URL + "/api/v1/drivers")
	if err != nil {
		t.Fatal(err)
	}
	var ds []string
	_ = json.NewDecoder(resp.Body).Decode(&ds)
	resp.Body.Close()
	if len(ds) == 0 {
		t.Fatal("no drivers")
	}

	// Create credential.
	credBody, _ := json.Marshal(map[string]string{"name": "c", "username": "u", "secret": "p"})
	resp, _ = c.Post(srv.URL+"/api/v1/credentials", "application/json", bytes.NewReader(credBody))
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("cred create: %s %s", resp.Status, b)
	}
	var cred map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&cred)
	resp.Body.Close()
	credID := int64(cred["id"].(float64))

	// Create device.
	devBody, _ := json.Marshal(map[string]any{
		"hostname": "r1", "address": "10.0.0.1", "port": 22,
		"driver": "cisco_ios", "credential_id": credID,
	})
	resp, _ = c.Post(srv.URL+"/api/v1/devices", "application/json", bytes.NewReader(devBody))
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("device create: %s %s", resp.Status, b)
	}
	var dev map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&dev)
	resp.Body.Close()
	devID := int64(dev["id"].(float64))

	// Trigger backup.
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/devices/"+itoa(devID)+"/backup", nil)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("backup: %s %s", resp.Status, b)
	}
	var run map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&run)
	resp.Body.Close()
	if run["status"] != "success" {
		t.Fatalf("status: %v", run["status"])
	}

	// Read latest config back.
	resp, _ = c.Get(srv.URL + "/api/v1/devices/" + itoa(devID) + "/config")
	if resp.StatusCode != 200 {
		t.Fatalf("config: %s", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte("hostname r1")) {
		t.Fatalf("config body: %q", body)
	}

	// Logout.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/logout", nil)
	resp, _ = c.Do(req)
	resp.Body.Close()
	resp, _ = c.Get(srv.URL + "/api/v1/devices")
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 after logout, got %s", resp.Status)
	}
}

func TestHealthAndOpenAPI(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/healthz")
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: %s", resp.Status)
	}
	resp, _ = http.Get(srv.URL + "/api/openapi.yaml")
	if resp.StatusCode != 200 {
		t.Fatalf("openapi: %s", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("openapi:")) {
		t.Fatalf("not yaml: %q", body[:min(80, len(body))])
	}
	resp, _ = http.Get(srv.URL + "/metrics")
	if resp.StatusCode != 200 {
		t.Fatalf("metrics: %s", resp.Status)
	}
}

func itoa(i int64) string {
	return formatInt(i)
}
