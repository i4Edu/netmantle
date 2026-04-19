package transport

import (
	"context"
	"net"
	"testing"

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
