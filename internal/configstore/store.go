// Package configstore stores device configurations as files inside a per-device
// git repository. Each successful backup becomes a single commit whose SHA is
// returned to the caller for cross-referencing in metadata tables.
//
// Layout on disk:
//
//	<root>/<tenant>/<device-id>/<artifact-name>
//
// Using one repo per device keeps history scoped, avoids cross-device merge
// noise, and makes a future "GitOps mirror" feature (push to a remote) a
// per-device decision.
package configstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Store writes versioned config artifacts to git repositories under Root.
type Store struct {
	Root string

	mu sync.Mutex // serialises commits across goroutines (per-process)
}

// New constructs a Store. The root directory is created if missing.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("configstore: empty root")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("configstore: mkdir root: %w", err)
	}
	return &Store{Root: root}, nil
}

// RepoPath returns the on-disk path of a device's git repository. The
// directory may not exist yet (no Commit has been made).
func (s *Store) RepoPath(tenantID, deviceID int64) (string, error) {
	if s == nil || s.Root == "" {
		return "", errors.New("configstore: not initialised")
	}
	return filepath.Join(s.Root,
		strconv.FormatInt(tenantID, 10),
		strconv.FormatInt(deviceID, 10),
	), nil
}

// Artifact is the input to Commit.
type Artifact struct {
	Name    string // file name within the device repo
	Content []byte
}

// CommitResult describes a successful commit.
type CommitResult struct {
	SHA       string
	Files     []string
	Timestamp time.Time
}

// Commit writes the supplied artifacts into the device's repository and
// records a single commit. If no artifact contents differ from the working
// tree, no commit is made and ErrNoChange is returned.
var ErrNoChange = errors.New("configstore: no changes")

func (s *Store) Commit(tenantID, deviceID int64, deviceName, author string, artifacts []Artifact) (*CommitResult, error) {
	if len(artifacts) == 0 {
		return nil, errors.New("configstore: no artifacts")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	repoPath := filepath.Join(s.Root,
		strconv.FormatInt(tenantID, 10),
		strconv.FormatInt(deviceID, 10),
	)
	if err := os.MkdirAll(repoPath, 0o750); err != nil {
		return nil, fmt.Errorf("configstore: mkdir device: %w", err)
	}

	repo, err := git.PlainOpen(repoPath)
	if err == git.ErrRepositoryNotExists {
		repo, err = git.PlainInit(repoPath, false)
		if err != nil {
			return nil, fmt.Errorf("configstore: git init: %w", err)
		}
		// Configure a default branch name.
		if cfg, cerr := repo.Config(); cerr == nil {
			cfg.Init.DefaultBranch = "main"
			_ = repo.SetConfig(cfg)
		}
	} else if err != nil {
		return nil, fmt.Errorf("configstore: git open: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("configstore: worktree: %w", err)
	}

	var files []string
	for _, a := range artifacts {
		if a.Name == "" {
			return nil, errors.New("configstore: artifact missing name")
		}
		// Disallow path traversal in artifact names.
		clean := filepath.Clean(a.Name)
		if clean != a.Name || filepath.IsAbs(clean) || hasParent(clean) {
			return nil, fmt.Errorf("configstore: invalid artifact name %q", a.Name)
		}
		dst := filepath.Join(repoPath, clean)
		if err := os.WriteFile(dst, a.Content, 0o640); err != nil {
			return nil, fmt.Errorf("configstore: write %s: %w", clean, err)
		}
		if _, err := wt.Add(clean); err != nil {
			return nil, fmt.Errorf("configstore: git add %s: %w", clean, err)
		}
		files = append(files, clean)
	}

	st, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("configstore: status: %w", err)
	}
	if st.IsClean() {
		return nil, ErrNoChange
	}

	now := time.Now().UTC()
	hash, err := wt.Commit(fmt.Sprintf("backup: %s", deviceName), &git.CommitOptions{
		Author: &object.Signature{
			Name:  author,
			Email: author + "@netmantle.local",
			When:  now,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configstore: commit: %w", err)
	}
	return &CommitResult{SHA: hash.String(), Files: files, Timestamp: now}, nil
}

// Read returns the content of the named artifact at HEAD for a device, or
// at the supplied commit SHA if non-empty.
func (s *Store) Read(tenantID, deviceID int64, artifact, sha string) ([]byte, error) {
	repoPath := filepath.Join(s.Root,
		strconv.FormatInt(tenantID, 10),
		strconv.FormatInt(deviceID, 10),
	)
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("configstore: open: %w", err)
	}
	var commit *object.Commit
	if sha == "" {
		ref, err := repo.Head()
		if err != nil {
			return nil, fmt.Errorf("configstore: head: %w", err)
		}
		commit, err = repo.CommitObject(ref.Hash())
		if err != nil {
			return nil, err
		}
	} else {
		h, err := repo.ResolveRevision(plumbing.Revision(sha))
		if err != nil {
			return nil, fmt.Errorf("configstore: resolve %s: %w", sha, err)
		}
		commit, err = repo.CommitObject(*h)
		if err != nil {
			return nil, err
		}
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	f, err := tree.File(artifact)
	if err != nil {
		return nil, fmt.Errorf("configstore: file %s: %w", artifact, err)
	}
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	buf := make([]byte, f.Size)
	if _, err := readAll(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readAll fills buf, growing if necessary; we know exact size from git.
func readAll(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	off := 0
	for off < len(buf) {
		n, err := r.Read(buf[off:])
		off += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return off, nil
			}
			return off, err
		}
	}
	return off, nil
}

// silence "imported and not used" possibilities while keeping the dep.
var _ = plumbing.ZeroHash

// hasParent reports whether p contains a ".." path component.
func hasParent(p string) bool {
	for _, seg := range strings.Split(p, string(os.PathSeparator)) {
		if seg == ".." {
			return true
		}
	}
	return false
}
