// Package credentials stores per-tenant device credentials. Secrets are
// encrypted at rest via internal/crypto's envelope format.
package credentials

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/crypto"
)

// Credential represents a reusable username + secret pair.
type Credential struct {
	ID        int64     `json:"id"`
	TenantID  int64     `json:"tenant_id"`
	Name      string    `json:"name"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// Repo wraps a *sql.DB + Sealer to provide encrypted credential CRUD.
type Repo struct {
	DB     *sql.DB
	Sealer *crypto.Sealer
}

// NewRepo constructs a Repo.
func NewRepo(db *sql.DB, s *crypto.Sealer) *Repo { return &Repo{DB: db, Sealer: s} }

// ErrNotFound is returned when a credential does not exist.
var ErrNotFound = errors.New("credentials: not found")

// Create stores a new credential, encrypting the secret on the way in.
func (r *Repo) Create(ctx context.Context, c Credential, secret string) (Credential, error) {
	if c.TenantID <= 0 || strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Username) == "" || secret == "" {
		return Credential{}, errors.New("credentials: tenant_id, name, username, secret required")
	}
	env, err := r.Sealer.Seal([]byte(secret))
	if err != nil {
		return Credential{}, err
	}
	now := time.Now().UTC()
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO credentials(tenant_id, name, username, secret_envelope, created_at) VALUES(?, ?, ?, ?, ?)`,
		c.TenantID, c.Name, c.Username, env, now.Format(time.RFC3339))
	if err != nil {
		return Credential{}, err
	}
	id, _ := res.LastInsertId()
	c.ID = id
	c.CreatedAt = now
	return c, nil
}

// Get returns metadata only (no secret).
func (r *Repo) Get(ctx context.Context, tenantID, id int64) (Credential, error) {
	var (
		c  Credential
		ts string
	)
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, username, created_at FROM credentials WHERE tenant_id=? AND id=?`,
		tenantID, id,
	).Scan(&c.ID, &c.TenantID, &c.Name, &c.Username, &ts)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Credential{}, ErrNotFound
		}
		return Credential{}, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return c, nil
}

// List returns all credentials for a tenant (no secrets).
func (r *Repo) List(ctx context.Context, tenantID int64) ([]Credential, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, username, created_at FROM credentials WHERE tenant_id=? ORDER BY name ASC`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var (
			c  Credential
			ts string
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Username, &ts); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Reveal decrypts and returns username + secret. Callers MUST treat the
// result as sensitive: never log it, redact it from errors, and avoid
// passing it through any tracing instrumentation.
func (r *Repo) Reveal(ctx context.Context, tenantID, id int64) (username, secret string, err error) {
	var env string
	err = r.DB.QueryRowContext(ctx,
		`SELECT username, secret_envelope FROM credentials WHERE tenant_id=? AND id=?`,
		tenantID, id,
	).Scan(&username, &env)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", err
	}
	pt, err := r.Sealer.Open(env)
	if err != nil {
		return "", "", err
	}
	return username, string(pt), nil
}

// Delete removes a credential.
func (r *Repo) Delete(ctx context.Context, tenantID, id int64) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM credentials WHERE tenant_id=? AND id=?`, tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
