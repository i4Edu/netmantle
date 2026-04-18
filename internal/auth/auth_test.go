package auth

import (
	"context"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(db, "", "test_session", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBootstrapAdminAndAuthenticate(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	user, pw, created, err := s.EnsureBootstrapAdmin(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || user != "admin" || pw == "" {
		t.Fatalf("unexpected: %v %q %q", created, user, pw)
	}
	// Idempotent.
	_, _, created2, err := s.EnsureBootstrapAdmin(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("should not recreate")
	}
	// Authenticate.
	u, err := s.Authenticate(ctx, "admin", pw)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.Role != RoleAdmin {
		t.Fatalf("role: %s", u.Role)
	}
	// Wrong password.
	if _, err := s.Authenticate(ctx, "admin", "nope"); err == nil {
		t.Fatal("expected auth failure")
	}
	if _, err := s.Authenticate(ctx, "ghost", "x"); err == nil {
		t.Fatal("expected auth failure for unknown user")
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	_, pw, _, _ := s.EnsureBootstrapAdmin(ctx, "preset-password-123")
	if pw != "preset-password-123" {
		t.Fatalf("preset ignored: %q", pw)
	}
	u, _ := s.Authenticate(ctx, "admin", pw)
	cookie, exp, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Before(time.Now()) {
		t.Fatal("expiry in past")
	}
	got, err := s.LookupSession(ctx, cookie)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("user mismatch")
	}
	// Tampered cookie.
	if _, err := s.LookupSession(ctx, cookie+"x"); err == nil {
		t.Fatal("expected tamper rejection")
	}
	// Destroy.
	if err := s.DestroySession(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, cookie); err == nil {
		t.Fatal("session should be gone")
	}
}

func TestRoles(t *testing.T) {
	if !RoleAdmin.CanWrite() || !RoleAdmin.CanAdmin() {
		t.Error("admin")
	}
	if !RoleOperator.CanWrite() || RoleOperator.CanAdmin() {
		t.Error("operator")
	}
	if RoleViewer.CanWrite() {
		t.Error("viewer should not write")
	}
	if Role("nope").IsValid() {
		t.Error("validity")
	}
}
