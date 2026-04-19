// Package poller implements the registration + heartbeat side of the
// remote-poller protocol (Phase 7).
//
// In a fully distributed deployment, lightweight pollers (separate
// binaries) connect *outbound* to the core over mTLS gRPC, register their
// zone, claim work from the queue, and report results. This package ships
// the persistence layer for that protocol — registration with a
// bcrypt-hashed bootstrap token, heartbeat tracking, and tenant-scoped
// listing — leaving the on-the-wire gRPC implementation as a follow-up.
package poller

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Poller is a registered remote poller.
type Poller struct {
	ID        int64     `json:"id"`
	TenantID  int64     `json:"tenant_id"`
	Zone      string    `json:"zone"`
	Name      string    `json:"name"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Service owns Poller registration + heartbeat.
type Service struct{ DB *sql.DB }

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Register creates a new poller and returns the bootstrap token. The token
// is shown to the operator exactly once; the DB stores only its bcrypt
// hash.
func (s *Service) Register(ctx context.Context, tenantID int64, zone, name string) (Poller, string, error) {
	if name == "" || zone == "" {
		return Poller{}, "", errors.New("poller: zone and name required")
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Poller{}, "", err
	}
	token := hex.EncodeToString(tokenBytes)
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return Poller{}, "", err
	}
	now := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO pollers(tenant_id, zone, name, token_hash, created_at) VALUES(?, ?, ?, ?, ?)`,
		tenantID, zone, name, string(hash), now.Format(time.RFC3339))
	if err != nil {
		return Poller{}, "", err
	}
	id, _ := res.LastInsertId()
	return Poller{ID: id, TenantID: tenantID, Zone: zone, Name: name, CreatedAt: now}, token, nil
}

// Authenticate verifies a poller's presented token and updates last_seen.
// Returns the poller record on success.
func (s *Service) Authenticate(ctx context.Context, tenantID int64, name, token string) (Poller, error) {
	var (
		p    Poller
		hash string
		ts   string
		last sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, zone, name, token_hash, created_at, last_seen FROM pollers WHERE tenant_id=? AND name=?`,
		tenantID, name).Scan(&p.ID, &p.TenantID, &p.Zone, &p.Name, &hash, &ts, &last)
	if err != nil {
		return Poller{}, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)); err != nil {
		return Poller{}, errors.New("poller: invalid token")
	}
	now := time.Now().UTC()
	_, _ = s.DB.ExecContext(ctx, `UPDATE pollers SET last_seen=? WHERE id=?`, now.Format(time.RFC3339), p.ID)
	p.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	p.LastSeen = now
	return p, nil
}

// List returns all pollers for a tenant.
func (s *Service) List(ctx context.Context, tenantID int64) ([]Poller, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, zone, name, IFNULL(last_seen,''), created_at FROM pollers WHERE tenant_id=? ORDER BY zone, name`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Poller
	for rows.Next() {
		var (
			p           Poller
			lastTS, cTS string
		)
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Zone, &p.Name, &lastTS, &cTS); err != nil {
			return nil, err
		}
		if lastTS != "" {
			p.LastSeen, _ = time.Parse(time.RFC3339, lastTS)
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, cTS)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a poller.
func (s *Service) Delete(ctx context.Context, tenantID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM pollers WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}
