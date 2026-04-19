package apitokens

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func setup(t *testing.T) (*Service, int64, int64) {
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
	res, _ = db.Exec(`INSERT INTO users(tenant_id, username, password_hash, role, created_at) VALUES(?, 'owner', 'x', 'admin', '2026-01-01T00:00:00Z')`, tid)
	uid, _ := res.LastInsertId()
	return New(db), tid, uid
}

func TestIssueAuthenticateRevoke(t *testing.T) {
	svc, tid, uid := setup(t)
	ctx := context.Background()
	tok, secret, err := svc.Issue(ctx, tid, uid, "billing", []string{"device:read", "changereq:approve"}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(secret, "nmt_") {
		t.Fatalf("secret format: %q", secret)
	}
	if tok.Prefix == "" || strings.Contains(secret, tok.Prefix) == false {
		t.Fatalf("prefix not embedded: %q vs %q", secret, tok.Prefix)
	}
	got, err := svc.Authenticate(ctx, secret)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if got.ID != tok.ID || got.TenantID != tid || !HasScope(got.Scopes, "device:read") {
		t.Fatalf("authenticated token mismatch: %+v", got)
	}
	// last_used_at populated.
	listed, _ := svc.List(ctx, tid)
	if len(listed) != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("last_used_at not updated: %+v", listed)
	}
	// Tampered secret rejected.
	if _, err := svc.Authenticate(ctx, secret+"x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for tampered, got %v", err)
	}
	// Revoke and re-auth fails.
	if err := svc.Revoke(ctx, tid, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid after revoke, got %v", err)
	}
	// Double revoke → not found.
	if err := svc.Revoke(ctx, tid, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on double revoke, got %v", err)
	}
}

func TestIssueValidatesInput(t *testing.T) {
	svc, tid, uid := setup(t)
	ctx := context.Background()
	if _, _, err := svc.Issue(ctx, 0, uid, "x", nil, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for tenant=0, got %v", err)
	}
	if _, _, err := svc.Issue(ctx, tid, uid, "", nil, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for empty name, got %v", err)
	}
}

func TestExpired(t *testing.T) {
	svc, tid, uid := setup(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	_, secret, err := svc.Issue(ctx, tid, uid, "expired", nil, &past)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for expired token, got %v", err)
	}
}

func TestSecretIsHashedAtRest(t *testing.T) {
	svc, tid, uid := setup(t)
	ctx := context.Background()
	_, secret, err := svc.Issue(ctx, tid, uid, "x", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := svc.DB.QueryRow(`SELECT secret_hash FROM api_tokens WHERE tenant_id=?`, tid).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, secret) {
		t.Fatalf("secret persisted in cleartext (or empty hash): %q vs %q", stored, secret)
	}
}

func TestHasScope(t *testing.T) {
	if !HasScope([]string{"a", "b"}, "a") {
		t.Fatal()
	}
	if HasScope([]string{"a"}, "b") {
		t.Fatal()
	}
	if !HasScope([]string{"*"}, "anything") {
		t.Fatal("wildcard")
	}
}
