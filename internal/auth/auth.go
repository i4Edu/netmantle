// Package auth implements local user authentication, signed session cookies,
// and a small RBAC layer (admin / operator / viewer).
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Role is the assigned RBAC role for a user.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// IsValid reports whether r is a known role.
func (r Role) IsValid() bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// CanWrite returns true for roles allowed to mutate resources.
func (r Role) CanWrite() bool { return r == RoleAdmin || r == RoleOperator }

// CanAdmin returns true for the admin role.
func (r Role) CanAdmin() bool { return r == RoleAdmin }

// User is the identity attached to an authenticated request.
type User struct {
	ID       int64  `json:"id"`
	TenantID int64  `json:"tenant_id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

// Service is the auth service: user lookup, password verification, session
// management. It is safe for concurrent use.
type Service struct {
	db         *sql.DB
	signingKey []byte
	cookieName string
	ttl        time.Duration
}

// NewService constructs an auth Service. signingKey may be empty, in which
// case a random key is generated for the lifetime of the process.
func NewService(db *sql.DB, signingKey, cookieName string, ttl time.Duration) (*Service, error) {
	if cookieName == "" {
		cookieName = "netmantle_session"
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	key := []byte(signingKey)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("auth: generate key: %w", err)
		}
	}
	return &Service{db: db, signingKey: key, cookieName: cookieName, ttl: ttl}, nil
}

// CookieName returns the configured session cookie name.
func (s *Service) CookieName() string { return s.cookieName }

// SessionTTL returns the configured session lifetime.
func (s *Service) SessionTTL() time.Duration { return s.ttl }

// HashPassword returns a bcrypt hash for the supplied plaintext.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// EnsureBootstrapAdmin creates the default tenant and an admin user if no
// users exist yet. It returns (username, generatedPassword, true) when an
// account was created, or ("", "", false) when one already existed.
//
// If preset is non-empty it is used as the password; otherwise a random
// 24-character password is generated and returned to the caller, which is
// expected to log it once.
func (s *Service) EnsureBootstrapAdmin(ctx context.Context, preset string) (string, string, bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return "", "", false, err
	}
	if n > 0 {
		return "", "", false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `INSERT INTO tenants(name, created_at) VALUES(?, ?)`, "default", now)
	if err != nil {
		return "", "", false, err
	}
	tenantID, err := res.LastInsertId()
	if err != nil {
		return "", "", false, err
	}
	pw := preset
	if pw == "" {
		pw = randomPassword()
	}
	hash, err := HashPassword(pw)
	if err != nil {
		return "", "", false, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO users(tenant_id, username, password_hash, role, created_at) VALUES(?, ?, ?, ?, ?)`,
		tenantID, "admin", hash, string(RoleAdmin), now); err != nil {
		return "", "", false, err
	}
	return "admin", pw, true, nil
}

// Authenticate verifies username/password and returns the user.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		u    User
		hash string
		role string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, username, password_hash, role FROM users WHERE username = ?`,
		username,
	).Scan(&u.ID, &u.TenantID, &u.Username, &hash, &role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	u.Role = Role(role)
	return &u, nil
}

// CreateSession stores a session row and returns a signed cookie value.
func (s *Service) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	id, err := randomID(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	exp := now.Add(s.ttl)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)`,
		id, userID, now.Format(time.RFC3339), exp.Format(time.RFC3339)); err != nil {
		return "", time.Time{}, err
	}
	return s.signCookie(id), exp, nil
}

// LookupSession verifies a cookie value and returns the associated user.
func (s *Service) LookupSession(ctx context.Context, cookie string) (*User, error) {
	id, ok := s.unsignCookie(cookie)
	if !ok {
		return nil, ErrInvalidSession
	}
	var (
		u       User
		role    string
		expires string
	)
	err := s.db.QueryRowContext(ctx, `
        SELECT s.expires_at, u.id, u.tenant_id, u.username, u.role
        FROM sessions s JOIN users u ON s.user_id = u.id
        WHERE s.id = ?`, id,
	).Scan(&expires, &u.ID, &u.TenantID, &u.Username, &role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidSession
		}
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil, ErrInvalidSession
	}
	if time.Now().After(t) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return nil, ErrInvalidSession
	}
	u.Role = Role(role)
	return &u, nil
}

// DestroySession removes the session referenced by the supplied cookie.
func (s *Service) DestroySession(ctx context.Context, cookie string) error {
	id, ok := s.unsignCookie(cookie)
	if !ok {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// signCookie returns "id.signature" where signature is HMAC-SHA256(id).
func (s *Service) signCookie(id string) string {
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(id))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return id + "." + sig
}

func (s *Service) unsignCookie(v string) (string, bool) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return "", false
	}
	return parts[0], true
}

func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomPassword() string {
	b := make([]byte, 18) // 24 base64 chars
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Errors.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrInvalidSession     = errors.New("auth: invalid session")
	ErrMFARequired        = errors.New("auth: MFA code required")
	ErrInvalidMFACode     = errors.New("auth: invalid MFA code")
)

// MFAEnrollment holds the data returned when starting MFA setup.
type MFAEnrollment struct {
	Secret     string `json:"secret"`      // base32-encoded secret for manual entry
	OtpauthURL string `json:"otpauth_url"` // QR-code URI for authenticator apps
}

// EnrollMFA generates a new TOTP secret for userID and stores it in the DB
// (not yet active—call ConfirmMFA to activate after the user verifies).
// Calling EnrollMFA again overwrites any pending unconfirmed secret.
func (s *Service) EnrollMFA(ctx context.Context, userID int64, issuer, username string) (MFAEnrollment, error) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return MFAEnrollment{}, err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ? WHERE id = ?`, secret, userID)
	if err != nil {
		return MFAEnrollment{}, fmt.Errorf("auth: enroll mfa: %w", err)
	}
	return MFAEnrollment{
		Secret:     secret,
		OtpauthURL: TOTPOtpauthURL(issuer, username, secret),
	}, nil
}

// ConfirmMFA verifies that the user can produce a valid TOTP code for their
// enrolled secret. This must be called after EnrollMFA before the secret is
// considered active. Returns ErrInvalidMFACode on a wrong code.
//
// NOTE: after ConfirmMFA the secret is retained—it is also the "active" flag.
// DisableMFA removes it.
func (s *Service) ConfirmMFA(ctx context.Context, userID int64, code string) error {
	var secret sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret FROM users WHERE id = ?`, userID).Scan(&secret)
	if err != nil {
		return fmt.Errorf("auth: confirm mfa: %w", err)
	}
	if !secret.Valid || secret.String == "" {
		return errors.New("auth: no pending MFA enrollment")
	}
	if !VerifyTOTP(secret.String, code) {
		return ErrInvalidMFACode
	}
	return nil
}

// DisableMFA removes the TOTP secret for userID, turning off MFA.
func (s *Service) DisableMFA(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret = NULL WHERE id = ?`, userID)
	return err
}

// MFAEnabled reports whether the user with the given ID has MFA configured.
func (s *Service) MFAEnabled(ctx context.Context, userID int64) (bool, error) {
	var secret sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id = ?`, userID).Scan(&secret)
	if err != nil {
		return false, err
	}
	return secret.Valid && secret.String != "", nil
}

// AuthenticateMFA checks a TOTP code for the given user ID.
// Returns ErrInvalidMFACode if the code is wrong.
func (s *Service) AuthenticateMFA(ctx context.Context, userID int64, code string) error {
	var secret sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id = ?`, userID).Scan(&secret)
	if err != nil || !secret.Valid || secret.String == "" {
		return ErrInvalidMFACode
	}
	if !VerifyTOTP(secret.String, code) {
		return ErrInvalidMFACode
	}
	return nil
}

// CreateMFAChallenge stores a short-lived challenge token for a user who
// passed password auth but has MFA enabled. Returns the challenge ID.
func (s *Service) CreateMFAChallenge(ctx context.Context, userID int64) (string, error) {
	id, err := randomID(32)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mfa_challenges(id, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)`,
		id, userID, now.Format(time.RFC3339), exp.Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("auth: create mfa challenge: %w", err)
	}
	return id, nil
}

// RedeemMFAChallenge verifies a TOTP code against a challenge token and,
// on success, returns the associated User (challenge is consumed on success).
func (s *Service) RedeemMFAChallenge(ctx context.Context, challengeID, code string) (*User, error) {
	var (
		u       User
		role    string
		expires string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT c.expires_at, u.id, u.tenant_id, u.username, u.role, u.totp_secret
		FROM mfa_challenges c JOIN users u ON c.user_id = u.id
		WHERE c.id = ?`, challengeID,
	).Scan(&expires, &u.ID, &u.TenantID, &u.Username, &role, new(sql.NullString))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidMFACode
		}
		return nil, err
	}
	t, _ := time.Parse(time.RFC3339, expires)
	if time.Now().After(t) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE id = ?`, challengeID)
		return nil, ErrInvalidMFACode
	}
	// Verify TOTP.
	if err := s.AuthenticateMFA(ctx, u.ID, code); err != nil {
		return nil, err
	}
	// Consume challenge.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE id = ?`, challengeID)
	u.Role = Role(role)
	return &u, nil
}
