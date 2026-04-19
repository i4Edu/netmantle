// Package gitops implements optional mirroring of a tenant's per-device
// configuration repos to an external git remote (Phase 10 differentiator:
// "the network as a git clone").
package gitops

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/i4Edu/netmantle/internal/configstore"
	"github.com/i4Edu/netmantle/internal/crypto"
)

// Mirror is one tenant's mirror configuration.
type Mirror struct {
	TenantID   int64     `json:"tenant_id"`
	RemoteURL  string    `json:"remote_url"`
	Branch     string    `json:"branch"`
	LastPushAt time.Time `json:"last_push_at,omitempty"`
}

// Service stores Mirror config and pushes per-device repos.
type Service struct {
	DB     *sql.DB
	Store  *configstore.Store
	Sealer *crypto.Sealer
}

// New constructs a Service.
func New(db *sql.DB, store *configstore.Store, sealer *crypto.Sealer) *Service {
	return &Service{DB: db, Store: store, Sealer: sealer}
}

// Configure stores or updates a tenant's mirror, encrypting the credential
// at rest. token may be empty if the remote is reachable without auth.
func (s *Service) Configure(ctx context.Context, tenantID int64, remoteURL, branch, token string) error {
	if remoteURL == "" {
		return errors.New("gitops: remote_url required")
	}
	if branch == "" {
		branch = "main"
	}
	var env string
	if token != "" {
		e, err := s.Sealer.Seal([]byte(token))
		if err != nil {
			return err
		}
		env = e
	}
	_, err := s.DB.ExecContext(ctx, `
        INSERT INTO gitops_mirrors(tenant_id, remote_url, branch, secret_envelope)
        VALUES(?, ?, ?, ?)
        ON CONFLICT(tenant_id) DO UPDATE SET
            remote_url = excluded.remote_url,
            branch = excluded.branch,
            secret_envelope = excluded.secret_envelope`,
		tenantID, remoteURL, branch, env)
	return err
}

// Get returns the configured mirror for a tenant, if any.
func (s *Service) Get(ctx context.Context, tenantID int64) (*Mirror, error) {
	var (
		m  Mirror
		ts sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT tenant_id, remote_url, branch, last_push_at FROM gitops_mirrors WHERE tenant_id=?`,
		tenantID).Scan(&m.TenantID, &m.RemoteURL, &m.Branch, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ts.Valid {
		m.LastPushAt, _ = time.Parse(time.RFC3339, ts.String)
	}
	return &m, nil
}

// PushDevice mirrors one device's repo to the configured remote. Returns
// nil with no work if no mirror is configured.
func (s *Service) PushDevice(ctx context.Context, tenantID, deviceID int64) error {
	m, err := s.Get(ctx, tenantID)
	if err != nil || m == nil {
		return err
	}
	repoPath, err := s.Store.RepoPath(tenantID, deviceID)
	if err != nil {
		return err
	}
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return err
	}
	// Configure the remote (idempotent: delete + create).
	_ = r.DeleteRemote("netmantle-mirror")
	if _, err := r.CreateRemote(&config.RemoteConfig{
		Name: "netmantle-mirror",
		URLs: []string{m.RemoteURL},
	}); err != nil {
		return err
	}

	auth, err := s.auth(ctx, tenantID)
	if err != nil {
		return err
	}
	err = r.PushContext(ctx, &git.PushOptions{
		RemoteName: "netmantle-mirror",
		Auth:       auth,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return err
	}
	_, _ = s.DB.ExecContext(ctx,
		`UPDATE gitops_mirrors SET last_push_at=? WHERE tenant_id=?`,
		time.Now().UTC().Format(time.RFC3339), tenantID)
	return nil
}

func (s *Service) auth(ctx context.Context, tenantID int64) (*githttp.BasicAuth, error) {
	var env sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT secret_envelope FROM gitops_mirrors WHERE tenant_id=?`, tenantID,
	).Scan(&env); err != nil {
		return nil, err
	}
	if !env.Valid || env.String == "" {
		return nil, nil
	}
	pt, err := s.Sealer.Open(env.String)
	if err != nil {
		return nil, err
	}
	// Convention: the stored token is a "user:pass" pair.
	user, pass := "git", string(pt)
	return &githttp.BasicAuth{Username: user, Password: pass}, nil
}
