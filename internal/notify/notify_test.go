package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/storage"
)

func newSvc(t *testing.T) (*Service, int64) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', '2026-01-01T00:00:00Z')`)
	tid, _ := res.LastInsertId()
	sealer, _ := crypto.NewSealer("k")
	return New(db, sealer, slog.New(slog.NewTextHandler(io.Discard, nil))), tid
}

func TestWebhookDispatch(t *testing.T) {
	svc, tid := newSvc(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, _ := json.Marshal(WebhookConfig{URL: srv.URL})
	c, err := svc.CreateChannel(context.Background(), tid, "wh", "webhook", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateRule(context.Background(), tid, "all-changes", "change", c.ID); err != nil {
		t.Fatal(err)
	}
	svc.Dispatch(context.Background(), tid, Event{Kind: "change", Subject: "test", Body: "x", Timestamp: time.Now()})
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits = %d", hits)
	}

	// Mismatched event kind: no dispatch.
	svc.Dispatch(context.Background(), tid, Event{Kind: "compliance.transition"})
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected no further hits, got %d", hits)
	}
}

func TestEmailPasswordIsSealed(t *testing.T) {
	svc, tid := newSvc(t)
	cfg, _ := json.Marshal(map[string]any{
		"host": "smtp", "port": 25, "from": "a@b", "to": "c@d",
		"username": "u", "password": "plaintext",
	})
	c, err := svc.CreateChannel(context.Background(), tid, "mail", "email", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	_ = json.Unmarshal(c.Config, &stored)
	if _, ok := stored["password"]; ok {
		t.Fatal("plaintext password leaked into stored config")
	}
	env, _ := stored["password_envelope"].(string)
	if env == "" {
		t.Fatal("envelope missing")
	}
	pt, err := svc.Sealer.Open(env)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if string(pt) != "plaintext" {
		t.Fatalf("decrypted: %q", pt)
	}
}

func TestRejectBadKind(t *testing.T) {
	svc, tid := newSvc(t)
	if _, err := svc.CreateChannel(context.Background(), tid, "x", "carrier-pigeon", []byte(`{}`)); err == nil {
		t.Fatal("expected rejection")
	}
}

// TestWebhookURLIsSealed verifies threat-model gap T10:
// webhook / Slack URLs must be stored as sealed envelopes, never plaintext.
func TestWebhookURLIsSealed(t *testing.T) {
	svc, tid := newSvc(t)
	const rawURL = "https://hooks.example.com/secret-token"

	for _, kind := range []string{"webhook", "slack"} {
		cfg, _ := json.Marshal(map[string]string{"url": rawURL})
		ch, err := svc.CreateChannel(context.Background(), tid, "test-"+kind, kind, cfg)
		if err != nil {
			t.Fatalf("%s CreateChannel: %v", kind, err)
		}

		var stored map[string]any
		_ = json.Unmarshal(ch.Config, &stored)

		// Plaintext "url" must not be present in stored config.
		if u, ok := stored["url"].(string); ok && u != "" {
			t.Errorf("%s: plaintext url leaked into stored config: %s", kind, u)
		}
		// Sealed "url_envelope" must be present.
		env, _ := stored["url_envelope"].(string)
		if env == "" {
			t.Fatalf("%s: url_envelope missing from stored config", kind)
		}
		// The envelope must decrypt to the original URL.
		pt, err := svc.Sealer.Open(env)
		if err != nil {
			t.Fatalf("%s: unseal url_envelope: %v", kind, err)
		}
		if string(pt) != rawURL {
			t.Fatalf("%s: decrypted url = %q, want %q", kind, pt, rawURL)
		}
	}
}

// TestWebhookURLEnvelopeRoundTrips verifies that dispatch correctly unseals
// the stored URL and can reach the test HTTP server.
func TestWebhookURLEnvelopeRoundTrips(t *testing.T) {
	svc, tid := newSvc(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, _ := json.Marshal(map[string]string{"url": srv.URL})
	ch, err := svc.CreateChannel(context.Background(), tid, "sealed-wh", "webhook", cfg)
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := svc.CreateRule(context.Background(), tid, "r1", "ping", ch.ID); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	svc.Dispatch(context.Background(), tid, Event{Kind: "ping", Subject: "s", Body: "b", Timestamp: time.Now()})
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("expected 1 hit, got %d — sealed URL may not be decrypted correctly", n)
	}
}
