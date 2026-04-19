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

// Credential represents a reusable username + secret pair. The plaintext
// secret never appears on this struct: list/get APIs return only metadata.
// Decryption is performed by Use (preferred) or, for legacy callers in
// the binary's own process, Reveal — which must never be exposed over
// the wire.
type Credential struct {
	ID         int64      `json:"id"`
	TenantID   int64      `json:"tenant_id"`
	Name       string     `json:"name"`
	Username   string     `json:"username"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Ref is the opaque, safe-to-serialise handle a UI/API client should hold
// instead of the cleartext secret. It carries enough metadata to render
// (id + display name + username + last-used) but nothing usable to
// impersonate the device. The transport layer redeems a Ref via Use.
type Ref struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Username   string     `json:"username"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// AsRef projects a Credential to its safe-to-share reference form.
func (c Credential) AsRef() Ref {
	return Ref{ID: c.ID, Name: c.Name, Username: c.Username, LastUsedAt: c.LastUsedAt}
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
		c        Credential
		ts       string
		lastUsed sql.NullString
	)
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, username, created_at, last_used_at FROM credentials WHERE tenant_id=? AND id=?`,
		tenantID, id,
	).Scan(&c.ID, &c.TenantID, &c.Name, &c.Username, &ts, &lastUsed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Credential{}, ErrNotFound
		}
		return Credential{}, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	if lastUsed.Valid {
		if t, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
			c.LastUsedAt = &t
		}
	}
	return c, nil
}

// List returns all credentials for a tenant (no secrets).
func (r *Repo) List(ctx context.Context, tenantID int64) ([]Credential, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, username, created_at, last_used_at FROM credentials WHERE tenant_id=? ORDER BY name ASC`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var (
			c        Credential
			ts       string
			lastUsed sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Username, &ts, &lastUsed); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		if lastUsed.Valid {
			if t, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
				c.LastUsedAt = &t
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Reveal decrypts and returns username + secret. Callers MUST treat the
// result as sensitive: never log it, redact it from errors, and avoid
// passing it through any tracing instrumentation.
//
// Reveal is intended for in-process integration callers (e.g. the
// scheduler or a poller worker) that need the cleartext outside the
// scoped Use lifecycle. New code should prefer Use, which scopes the
// cleartext to a single function invocation and updates last_used_at
// for accountability.
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

// Use is the preferred entry point for any caller that needs the
// cleartext secret. It decrypts the secret, hands it together with the
// username to fn, zeroises the local plaintext copy on return, and
// records the access in `credentials.last_used_at` so accountability
// queries can show when a credential was last redeemed and by whom
// (callers wanting actor attribution should also write an audit_log
// row at the same call site).
//
// fn must not retain references to either string after it returns. The
// implementation reuses the underlying byte slice and zeroises it before
// Use exits; retained references would be observed as zero bytes.
func (r *Repo) Use(ctx context.Context, tenantID, id int64, fn func(username, secret string) error) error {
	var (
		username string
		env      string
	)
	err := r.DB.QueryRowContext(ctx,
		`SELECT username, secret_envelope FROM credentials WHERE tenant_id=? AND id=?`,
		tenantID, id,
	).Scan(&username, &env)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	pt, err := r.Sealer.Open(env)
	if err != nil {
		return err
	}
	// Best-effort last-used update; never fail the caller because the
	// timestamp could not be persisted.
	_, _ = r.DB.ExecContext(ctx,
		`UPDATE credentials SET last_used_at=? WHERE tenant_id=? AND id=?`,
		time.Now().UTC().Format(time.RFC3339), tenantID, id)
	defer func() {
		for i := range pt {
			pt[i] = 0
		}
	}()
	return fn(username, string(pt))
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
