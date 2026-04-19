package drivers

import (
	"context"
	"strings"
	"testing"
)

type fakeDriver struct{ name string }

func (f *fakeDriver) Name() string { return f.name }
func (f *fakeDriver) FetchConfig(ctx context.Context, s Session) ([]ConfigArtifact, error) {
	return nil, nil
}

func TestRegistry(t *testing.T) {
	reset()
	Register(&fakeDriver{name: "x"})
	if _, err := Get("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("nope"); err == nil {
		t.Fatal("expected error")
	}
	got := List()
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("list: %v", got)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	reset()
	Register(&fakeDriver{name: "dup"})
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "duplicate") {
			t.Fatalf("expected duplicate panic, got %v", r)
		}
	}()
	Register(&fakeDriver{name: "dup"})
}
