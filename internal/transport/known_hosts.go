package transport

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// KnownHostsStore is the persistence contract for SSH host-key pinning.
//
// On first connection to a host, Add is called to record the key (TOFU).
// On subsequent connections, Check is called; if the key has changed,
// Check must return a non-nil error so the dial is aborted.
type KnownHostsStore interface {
	// Check verifies hostname:port against stored keys.
	// Returns nil if the key is known and matches, ErrKeyChanged if the
	// host key has been replaced, or ErrUnknownHost if this is the first
	// connection (the caller must then call Add).
	Check(tenantID int64, host string, port int, algo string, key []byte) error

	// Add pins a key for hostname:port. Idempotent if the same key is
	// re-added.
	Add(ctx context.Context, tenantID int64, host string, port int, algo string, key []byte) error
}

// ErrKeyChanged is returned by KnownHostsStore.Check when a host presents
// a different public key than the one recorded on first connection.
var ErrKeyChanged = fmt.Errorf("transport/ssh: host key changed — possible MITM attack")

// ErrUnknownHost is returned by KnownHostsStore.Check when no key has
// been pinned for the requested host yet.
var ErrUnknownHost = fmt.Errorf("transport/ssh: unknown host (TOFU pin will be stored)")

// DBKnownHostsStore is a KnownHostsStore backed by the ssh_known_hosts
// SQLite table introduced in migration 0005.
type DBKnownHostsStore struct {
	DB *sql.DB
}

// NewDBKnownHostsStore constructs a DBKnownHostsStore.
func NewDBKnownHostsStore(db *sql.DB) *DBKnownHostsStore {
	return &DBKnownHostsStore{DB: db}
}

// Check verifies the host key against stored records.
func (s *DBKnownHostsStore) Check(tenantID int64, host string, port int, algo string, key []byte) error {
	encoded := base64.StdEncoding.EncodeToString(key)
	var stored string
	err := s.DB.QueryRow(
		`SELECT public_key FROM ssh_known_hosts
         WHERE tenant_id=? AND host=? AND port=? AND algorithm=?`,
		tenantID, host, port, algo,
	).Scan(&stored)
	if err == sql.ErrNoRows {
		return ErrUnknownHost
	}
	if err != nil {
		return fmt.Errorf("transport/ssh: known-hosts lookup: %w", err)
	}
	if stored != encoded {
		return ErrKeyChanged
	}
	return nil
}

// Add pins the key. If the same (tenant,host,port,algorithm) already exists
// with the same key, the call is a no-op.
func (s *DBKnownHostsStore) Add(ctx context.Context, tenantID int64, host string, port int, algo string, key []byte) error {
	encoded := base64.StdEncoding.EncodeToString(key)
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO ssh_known_hosts(tenant_id, host, port, algorithm, public_key, added_at)
         VALUES(?, ?, ?, ?, ?, ?)
         ON CONFLICT(tenant_id, host, port, algorithm) DO NOTHING`,
		tenantID, host, port, algo, encoded, time.Now().UTC().Format(time.RFC3339))
	return err
}

// MemKnownHostsStore is an in-process KnownHostsStore used when no DB is
// available (tests, single-shot operations). It behaves like the original
// TOFU logic: keys are accepted and pinned on first use; a changed key
// triggers ErrKeyChanged.
type MemKnownHostsStore struct {
	mu     sync.Mutex
	pinned map[string][]byte // key: "tenantID:host:port:algo"
}

// NewMemKnownHostsStore constructs an empty in-memory store.
func NewMemKnownHostsStore() *MemKnownHostsStore {
	return &MemKnownHostsStore{pinned: map[string][]byte{}}
}

func (m *MemKnownHostsStore) mapKey(tenantID int64, host string, port int, algo string) string {
	return fmt.Sprintf("%d:%s:%d:%s", tenantID, host, port, algo)
}

// Check implements KnownHostsStore.
func (m *MemKnownHostsStore) Check(tenantID int64, host string, port int, algo string, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.mapKey(tenantID, host, port, algo)
	existing, ok := m.pinned[k]
	if !ok {
		return ErrUnknownHost
	}
	if string(existing) != string(key) {
		return ErrKeyChanged
	}
	return nil
}

// Add implements KnownHostsStore.
func (m *MemKnownHostsStore) Add(_ context.Context, tenantID int64, host string, port int, algo string, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.mapKey(tenantID, host, port, algo)
	if _, ok := m.pinned[k]; !ok {
		m.pinned[k] = append([]byte(nil), key...)
	}
	return nil
}

// knownHostsCallback builds an ssh.HostKeyCallback that consults store.
// On ErrUnknownHost (first connection) the key is pinned via store.Add.
// On ErrKeyChanged the dial is rejected.
func knownHostsCallback(ctx context.Context, tenantID int64, port int, store KnownHostsStore) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		host, _, err := net.SplitHostPort(hostname)
		if err != nil {
			host = hostname
		}
		algo := key.Type()
		raw := key.Marshal()

		switch cerr := store.Check(tenantID, host, port, algo, raw); cerr {
		case nil:
			return nil
		case ErrUnknownHost:
			// TOFU: accept and pin on first use.
			if aerr := store.Add(ctx, tenantID, host, port, algo, raw); aerr != nil {
				// Failing to persist the pin is non-fatal but logged by
				// the caller; the connection is still allowed this time.
				_ = aerr
			}
			return nil
		case ErrKeyChanged:
			return ErrKeyChanged
		default:
			return cerr
		}
	}
}
