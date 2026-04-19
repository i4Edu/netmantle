package transport

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/i4Edu/netmantle/internal/storage"
	"golang.org/x/crypto/ssh"
)

// TestMemKnownHostsTOFU verifies that the MemKnownHostsStore accepts
// a key on first connection and rejects a different key on the second.
func TestMemKnownHostsTOFU(t *testing.T) {
	store := NewMemKnownHostsStore()
	ctx := context.Background()
	const tenantID = int64(1)
	host := "10.0.0.1"
	port := 22

	keyA := []byte("ed25519-key-bytes-A")
	keyB := []byte("ed25519-key-bytes-B")

	// First check: unknown host.
	if err := store.Check(tenantID, host, port, "ssh-ed25519", keyA); err != ErrUnknownHost {
		t.Fatalf("expected ErrUnknownHost, got %v", err)
	}
	// Add the key (TOFU).
	if err := store.Add(ctx, tenantID, host, port, "ssh-ed25519", keyA); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Second check: same key — accepted.
	if err := store.Check(tenantID, host, port, "ssh-ed25519", keyA); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	// Different key — rejected.
	if err := store.Check(tenantID, host, port, "ssh-ed25519", keyB); err != ErrKeyChanged {
		t.Fatalf("expected ErrKeyChanged, got %v", err)
	}
}

// TestMemKnownHostsTenantIsolation confirms that two tenants pinning the
// same host independently do not interfere with each other.
func TestMemKnownHostsTenantIsolation(t *testing.T) {
	store := NewMemKnownHostsStore()
	ctx := context.Background()
	host, port := "192.168.1.1", 22

	keyT1 := []byte("key-for-tenant-1")
	keyT2 := []byte("key-for-tenant-2")

	if err := store.Add(ctx, 1, host, port, "ssh-rsa", keyT1); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(ctx, 2, host, port, "ssh-rsa", keyT2); err != nil {
		t.Fatal(err)
	}

	// Tenant 1 sees its own key.
	if err := store.Check(1, host, port, "ssh-rsa", keyT1); err != nil {
		t.Fatalf("tenant1 check with correct key: %v", err)
	}
	// Tenant 1 does NOT see tenant 2's key.
	if err := store.Check(1, host, port, "ssh-rsa", keyT2); err != ErrKeyChanged {
		t.Fatalf("tenant1: expected ErrKeyChanged for tenant2 key, got %v", err)
	}
}

// TestResolveHostKeyCallbackFallback ensures that when no KnownHosts store
// is supplied, a MemKnownHostsStore is created and TOFU behaviour applies.
func TestResolveHostKeyCallbackFallback(t *testing.T) {
	cfg := SSHConfig{Address: "10.0.0.1", Port: 22, TenantID: 99}
	cb := resolveHostKeyCallback(context.Background(), cfg)
	if cb == nil {
		t.Fatal("expected non-nil HostKeyCallback")
	}
	// The callback must accept any key on the first call (TOFU).
	// We generate a real test host key to pass a valid ssh.PublicKey.
	signer := mustGenerateTestHostKey(t)
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}
	if err := cb("10.0.0.1:22", addr, signer.PublicKey()); err != nil {
		t.Fatalf("first TOFU call: %v", err)
	}
	// Same key on second call must also be accepted (same in-memory store).
	if err := cb("10.0.0.1:22", addr, signer.PublicKey()); err != nil {
		t.Fatalf("second TOFU call (same key): %v", err)
	}
}

// TestExplicitHostKeyCallbackHonoured verifies that cfg.HostKeyCallback
// takes precedence over cfg.KnownHosts.
func TestExplicitHostKeyCallbackHonoured(t *testing.T) {
	var called int
	override := ssh.HostKeyCallback(func(_ string, _ net.Addr, _ ssh.PublicKey) error {
		called++
		return nil
	})
	cfg := SSHConfig{
		HostKeyCallback: override,
		KnownHosts:      NewMemKnownHostsStore(), // should be ignored
	}
	cb := resolveHostKeyCallback(context.Background(), cfg)
	signer := mustGenerateTestHostKey(t)
	addr := &net.TCPAddr{}
	_ = cb("h:22", addr, signer.PublicKey())
	if called != 1 {
		t.Fatalf("override callback not called; called=%d", called)
	}
}

// mustGenerateTestHostKey generates an ephemeral ed25519 SSH host key for
// use in tests.
func mustGenerateTestHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	// Use Ed25519 for test host keys.
	_, priv, err := generateTestEd25519Key()
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}

func openKnownHostsDB(t *testing.T, dsn string) *DBKnownHostsStore {
	t.Helper()
	db, err := storage.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := storage.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	return NewDBKnownHostsStore(db)
}

func seedTenantRow(t *testing.T, store *DBKnownHostsStore, tenantID int64) {
	t.Helper()
	_, err := store.DB.Exec(`INSERT OR IGNORE INTO tenants(id, name, created_at) VALUES(?, ?, ?)`,
		tenantID, "tenant", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
}

func TestDBKnownHostsStoreFailoverConsistency(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "known-hosts-ha.db")

	primary := openKnownHostsDB(t, dbPath)
	defer primary.DB.Close()
	seedTenantRow(t, primary, 1)
	keyA := []byte("ssh-ed25519-primary")
	if err := primary.Add(context.Background(), 1, "edge1", 22, "ssh-ed25519", keyA); err != nil {
		t.Fatalf("primary Add: %v", err)
	}

	secondary := openKnownHostsDB(t, dbPath)
	defer secondary.DB.Close()
	seedTenantRow(t, secondary, 1)

	if err := secondary.Check(1, "edge1", 22, "ssh-ed25519", keyA); err != nil {
		t.Fatalf("secondary Check: %v", err)
	}
	// Idempotent replay after failover should not produce mismatches.
	if err := secondary.Add(context.Background(), 1, "edge1", 22, "ssh-ed25519", keyA); err != nil {
		t.Fatalf("secondary Add same key: %v", err)
	}
	if err := secondary.Check(1, "edge1", 22, "ssh-ed25519", []byte("ssh-ed25519-rotated")); err != ErrKeyChanged {
		t.Fatalf("expected ErrKeyChanged after failover, got %v", err)
	}
}

func TestDBKnownHostsStoreFailoverTenantIsolation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "known-hosts-tenants.db")

	storeA := openKnownHostsDB(t, dbPath)
	defer storeA.DB.Close()
	seedTenantRow(t, storeA, 1)
	seedTenantRow(t, storeA, 2)

	if err := storeA.Add(context.Background(), 1, "edge2", 22, "ssh-rsa", []byte("tenant1-key")); err != nil {
		t.Fatal(err)
	}

	storeB := openKnownHostsDB(t, dbPath)
	defer storeB.DB.Close()
	seedTenantRow(t, storeB, 1)
	seedTenantRow(t, storeB, 2)
	if err := storeB.Add(context.Background(), 2, "edge2", 22, "ssh-rsa", []byte("tenant2-key")); err != nil {
		t.Fatal(err)
	}
	if err := storeB.Check(1, "edge2", 22, "ssh-rsa", []byte("tenant1-key")); err != nil {
		t.Fatalf("tenant1 check: %v", err)
	}
	if err := storeB.Check(1, "edge2", 22, "ssh-rsa", []byte("tenant2-key")); err != ErrKeyChanged {
		t.Fatalf("tenant isolation mismatch: got %v", err)
	}
}
