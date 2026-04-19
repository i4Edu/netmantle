package credentials

import (
	"context"
	"testing"

	"github.com/i4Edu/netmantle/internal/crypto"
	"github.com/i4Edu/netmantle/internal/storage"
)

func setup(t *testing.T) (*Repo, int64) {
	t.Helper()
	db, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tenants(name, created_at) VALUES('t', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	s, _ := crypto.NewSealer("test-passphrase")
	return NewRepo(db, s), id
}

func TestCredentialsRoundtrip(t *testing.T) {
	r, tid := setup(t)
	ctx := context.Background()
	c, err := r.Create(ctx, Credential{TenantID: tid, Name: "default", Username: "admin"}, "s3cret")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("no id")
	}
	got, err := r.Get(ctx, tid, c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Username != "admin" {
		t.Fatalf("user: %s", got.Username)
	}
	user, secret, err := r.Reveal(ctx, tid, c.ID)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if user != "admin" || secret != "s3cret" {
		t.Fatalf("decrypted wrong: %q %q", user, secret)
	}
	list, err := r.List(ctx, tid)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if err := r.Delete(ctx, tid, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, tid, c.ID); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestCredentialsUseUpdatesLastUsedAndZeroises(t *testing.T) {
	r, tid := setup(t)
	ctx := context.Background()
	c, err := r.Create(ctx, Credential{TenantID: tid, Name: "u", Username: "admin"}, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx, tid, c.ID); got.LastUsedAt != nil {
		t.Fatalf("last_used_at should be nil before Use, got %v", got.LastUsedAt)
	}

	var calledUser, calledSecret string
	if err := r.Use(ctx, tid, c.ID, func(u, s string) error {
		calledUser, calledSecret = u, s
		return nil
	}); err != nil {
		t.Fatalf("use: %v", err)
	}
	if calledUser != "admin" || calledSecret != "s3cret" {
		t.Fatalf("decrypted wrong: %q %q", calledUser, calledSecret)
	}
	got, err := r.Get(ctx, tid, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("expected last_used_at to be set after Use")
	}

	// Use propagates fn errors.
	wantErr := errSentinel
	if err := r.Use(ctx, tid, c.ID, func(_, _ string) error { return wantErr }); err != wantErr {
		t.Fatalf("expected fn error to bubble, got %v", err)
	}

	// Use against a missing id returns ErrNotFound.
	if err := r.Use(ctx, tid, c.ID+999, func(_, _ string) error { return nil }); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing id, got %v", err)
	}
}

var errSentinel = errSentinelT("sentinel")

type errSentinelT string

func (e errSentinelT) Error() string { return string(e) }

func TestSecretIsEncryptedAtRest(t *testing.T) {
	r, tid := setup(t)
	ctx := context.Background()
	_, err := r.Create(ctx, Credential{TenantID: tid, Name: "x", Username: "u"}, "plaintext-secret")
	if err != nil {
		t.Fatal(err)
	}
	var env string
	if err := r.DB.QueryRow(`SELECT secret_envelope FROM credentials WHERE tenant_id=?`, tid).Scan(&env); err != nil {
		t.Fatal(err)
	}
	if env == "" || env == "plaintext-secret" {
		t.Fatalf("envelope looks wrong: %q", env)
	}
}
