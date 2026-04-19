package configstore

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCommitAndRead(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Commit(1, 42, "r1", "tester", []Artifact{
		{Name: "running-config", Content: []byte("hostname r1\n")},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.SHA == "" {
		t.Fatal("empty sha")
	}
	got, err := s.Read(1, 42, "running-config", "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hostname r1\n" {
		t.Fatalf("content: %q", got)
	}
	// Same content -> ErrNoChange.
	if _, err := s.Commit(1, 42, "r1", "tester", []Artifact{
		{Name: "running-config", Content: []byte("hostname r1\n")},
	}); !errors.Is(err, ErrNoChange) {
		t.Fatalf("expected ErrNoChange, got %v", err)
	}
	// Modified content -> new commit.
	res2, err := s.Commit(1, 42, "r1", "tester", []Artifact{
		{Name: "running-config", Content: []byte("hostname r1-new\n")},
	})
	if err != nil {
		t.Fatalf("commit2: %v", err)
	}
	if res2.SHA == res.SHA {
		t.Fatal("expected new SHA")
	}
	// Read previous version by SHA.
	old, err := s.Read(1, 42, "running-config", res.SHA)
	if err != nil {
		t.Fatalf("read old: %v", err)
	}
	if string(old) != "hostname r1\n" {
		t.Fatalf("old content: %q", old)
	}
}

func TestCommitRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.Commit(1, 1, "d", "t", []Artifact{
		{Name: "../escape", Content: []byte("x")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = s.Commit(1, 1, "d", "t", []Artifact{
		{Name: filepath.Join("a", ".."), Content: []byte("x")},
	})
	if err == nil {
		t.Fatal("expected error for traversal")
	}
}
