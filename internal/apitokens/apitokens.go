// Package apitokens issues, validates and revokes long-lived bearer
// tokens for machine-to-machine integrations (billing, provisioning,
// CI pipelines, …).
//
// Tokens are emitted as `nmt_<prefix>_<secret>`:
//   - `nmt`   is a fixed scheme tag used to disambiguate from the
//     poller bootstrap tokens defined in internal/poller.
//   - `prefix` is a 12-char base32 lookup key, indexable in the DB.
//   - `secret` is a 32-char base64url random string. Only its bcrypt
//     hash is persisted; the cleartext is returned to the caller exactly
//     once at issue time.
//
// Authentication is a constant-time bcrypt compare against the row
// matching `prefix`. Revoked or expired tokens fail with ErrInvalid.
package apitokens

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	scheme       = "nmt"
	prefixLen    = 12
	secretBytes  = 24 // → 32 base64url chars
	bcryptCost   = bcrypt.DefaultCost
	maxNameBytes = 128
)

// Token is an issued API token (metadata only). The cleartext value is
// returned by Issue and never persisted; re-fetching a Token returns
// only what's safe to display in a UI.
type Token struct {
	ID          int64      `json:"id"`
	TenantID    int64      `json:"tenant_id"`
	OwnerUserID int64      `json:"owner_user_id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// Service owns api_tokens persistence + verification.
type Service struct {
	DB *sql.DB
}

// New constructs a Service.
func New(db *sql.DB) *Service { return &Service{DB: db} }

// Errors.
var (
	ErrNotFound     = errors.New("apitokens: not found")
	ErrInvalid      = errors.New("apitokens: invalid token")
	ErrInvalidInput = errors.New("apitokens: invalid input")
)

// Issue creates a new token. The returned plaintext value is the only
// time the secret half is visible; callers must surface it to the user
// immediately and warn that it cannot be recovered.
func (s *Service) Issue(ctx context.Context, tenantID, ownerUserID int64, name string, scopes []string, expiresAt *time.Time) (Token, string, error) {
	name = strings.TrimSpace(name)
	switch {
	case tenantID <= 0 || ownerUserID <= 0:
		return Token{}, "", fmt.Errorf("%w: tenant and owner required", ErrInvalidInput)
	case name == "" || len(name) > maxNameBytes:
		return Token{}, "", fmt.Errorf("%w: name length", ErrInvalidInput)
	}
	prefix, secret, err := generate()
	if err != nil {
		return Token{}, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcryptCost)
	if err != nil {
		return Token{}, "", err
	}
	now := time.Now().UTC()
	var expArg any
	if expiresAt != nil {
		expArg = expiresAt.UTC().Format(time.RFC3339)
	}
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO api_tokens(tenant_id, owner_user_id, name, prefix, secret_hash, scopes, created_at, expires_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, ownerUserID, name, prefix, string(hash), strings.Join(normaliseScopes(scopes), ","),
		now.Format(time.RFC3339), expArg)
	if err != nil {
		return Token{}, "", err
	}
	id, _ := res.LastInsertId()
	tok := Token{
		ID: id, TenantID: tenantID, OwnerUserID: ownerUserID, Name: name,
		Prefix: prefix, Scopes: normaliseScopes(scopes), CreatedAt: now, ExpiresAt: expiresAt,
	}
	return tok, formatToken(prefix, secret), nil
}

// Authenticate parses and validates a presented bearer string. On
// success the returned Token has up-to-date metadata; the call also
// updates last_used_at as a side-effect (best-effort).
func (s *Service) Authenticate(ctx context.Context, presented string) (Token, error) {
	prefix, secret, ok := parseToken(presented)
	if !ok {
		return Token{}, ErrInvalid
	}
	var (
		tok       Token
		hash      string
		scopesRaw string
		createdAt string
		expiresAt sql.NullString
		lastUsed  sql.NullString
		revokedAt sql.NullString
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, tenant_id, owner_user_id, name, prefix, secret_hash, scopes,
		       created_at, expires_at, last_used_at, revoked_at
		FROM api_tokens WHERE prefix=?`, prefix,
	).Scan(&tok.ID, &tok.TenantID, &tok.OwnerUserID, &tok.Name, &tok.Prefix, &hash, &scopesRaw,
		&createdAt, &expiresAt, &lastUsed, &revokedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, ErrInvalid
		}
		return Token{}, err
	}
	if revokedAt.Valid {
		return Token{}, ErrInvalid
	}
	tok.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAt.String)
		tok.ExpiresAt = &t
		if !t.IsZero() && time.Now().UTC().After(t) {
			return Token{}, ErrInvalid
		}
	}
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339, lastUsed.String)
		tok.LastUsedAt = &t
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) != nil {
		return Token{}, ErrInvalid
	}
	tok.Scopes = parseScopes(scopesRaw)
	// Best-effort touch of last_used_at; failure must not break auth.
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), tok.ID)
	return tok, nil
}

// List returns active and revoked tokens for a tenant (no secrets).
func (s *Service) List(ctx context.Context, tenantID int64) ([]Token, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, tenant_id, owner_user_id, name, prefix, scopes,
		       created_at, expires_at, last_used_at, revoked_at
		FROM api_tokens WHERE tenant_id=? ORDER BY id DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var (
			t         Token
			scopesRaw string
			createdAt string
			expiresAt sql.NullString
			lastUsed  sql.NullString
			revokedAt sql.NullString
		)
		if err := rows.Scan(&t.ID, &t.TenantID, &t.OwnerUserID, &t.Name, &t.Prefix, &scopesRaw,
			&createdAt, &expiresAt, &lastUsed, &revokedAt); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if expiresAt.Valid {
			tt, _ := time.Parse(time.RFC3339, expiresAt.String)
			t.ExpiresAt = &tt
		}
		if lastUsed.Valid {
			tt, _ := time.Parse(time.RFC3339, lastUsed.String)
			t.LastUsedAt = &tt
		}
		if revokedAt.Valid {
			tt, _ := time.Parse(time.RFC3339, revokedAt.String)
			t.RevokedAt = &tt
		}
		t.Scopes = parseScopes(scopesRaw)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke marks a token revoked. Subsequent Authenticate calls fail.
func (s *Service) Revoke(ctx context.Context, tenantID, id int64) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at=? WHERE tenant_id=? AND id=? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HasScope reports whether the supplied scopes contain `want`. A scope
// of "*" is treated as a wildcard granting every action — admins
// receive this implicitly via the API handler glue.
func HasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}

// ---- internals ----

func generate() (prefix, secret string, err error) {
	pb := make([]byte, 8) // 8 bytes → 13 base32 chars; trim to 12
	if _, err := rand.Read(pb); err != nil {
		return "", "", err
	}
	prefix = strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(pb), "=")
	if len(prefix) > prefixLen {
		prefix = prefix[:prefixLen]
	}
	prefix = strings.ToLower(prefix)

	sb := make([]byte, secretBytes)
	if _, err := rand.Read(sb); err != nil {
		return "", "", err
	}
	secret = base64.RawURLEncoding.EncodeToString(sb)
	return prefix, secret, nil
}

func formatToken(prefix, secret string) string {
	return scheme + "_" + prefix + "_" + secret
}

func parseToken(s string) (prefix, secret string, ok bool) {
	parts := strings.SplitN(s, "_", 3)
	if len(parts) != 3 || parts[0] != scheme || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func normaliseScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parseScopes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
