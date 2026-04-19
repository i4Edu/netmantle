package diff

import (
	"regexp"
	"strings"
	"testing"
)

func TestIdenticalAfterIgnore(t *testing.T) {
	e := &Engine{Rules: DefaultRules()}
	a := "Building configuration...\nCurrent configuration : 100 bytes\nhostname r1\n"
	b := "Building configuration...\nCurrent configuration : 200 bytes\nhostname r1\n"
	r := e.Diff("running-config", a, b)
	if !r.Identical {
		t.Fatalf("expected identical, got %+v", r)
	}
}

func TestUnifiedDiffCounts(t *testing.T) {
	e := &Engine{}
	a := "a\nb\nc\n"
	b := "a\nB\nc\nd\n"
	r := e.Diff("x", a, b)
	if r.Identical {
		t.Fatal("should differ")
	}
	if r.AddedLines != 2 || r.RemovedLines != 1 {
		t.Fatalf("counts: +%d -%d", r.AddedLines, r.RemovedLines)
	}
	if !strings.Contains(r.Unified, "- b") || !strings.Contains(r.Unified, "+ B") || !strings.Contains(r.Unified, "+ d") {
		t.Fatalf("unified: %q", r.Unified)
	}
}

func TestArtifactPrefixScoping(t *testing.T) {
	e := &Engine{Rules: []IgnoreRule{{
		ArtifactPrefix: "startup-",
		Pattern:        regexp.MustCompile(`^marker$`),
	}}}
	r := e.Diff("running-config", "marker\n", "")
	if r.Identical {
		t.Fatal("scoped rule must not apply to running-config")
	}
	r = e.Diff("startup-config", "marker\n", "")
	if !r.Identical {
		t.Fatal("scoped rule should apply to startup-config")
	}
}
