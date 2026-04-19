package rulepacks_test

import (
	"testing"

	"github.com/i4Edu/netmantle/internal/compliance/rulepacks"
)

func TestAllPacksHaveRequiredFields(t *testing.T) {
	for name, pack := range rulepacks.All() {
		if pack.Name == "" {
			t.Errorf("pack %q has empty Name", name)
		}
		if pack.Version == "" {
			t.Errorf("pack %q has empty Version", name)
		}
		if pack.Description == "" {
			t.Errorf("pack %q has empty Description", name)
		}
		if len(pack.Rules) == 0 {
			t.Errorf("pack %q has no rules", name)
		}
		for i, r := range pack.Rules {
			if r.Name == "" {
				t.Errorf("pack %q rule[%d] has empty Name", name, i)
			}
			if r.Kind == "" {
				t.Errorf("pack %q rule[%d] has empty Kind", name, i)
			}
			if r.Pattern == "" {
				t.Errorf("pack %q rule[%d] has empty Pattern", name, i)
			}
		}
	}
}

func TestGetReturnsCorrectPack(t *testing.T) {
	pack, ok := rulepacks.Get("isp-baseline")
	if !ok {
		t.Fatal("isp-baseline not found")
	}
	if pack.Name != "isp-baseline" {
		t.Fatalf("got name %q", pack.Name)
	}
}

func TestGetMissingPackReturnsFalse(t *testing.T) {
	_, ok := rulepacks.Get("does-not-exist")
	if ok {
		t.Fatal("expected ok=false for unknown pack")
	}
}

func TestPackKeyMatchesName(t *testing.T) {
	for key, pack := range rulepacks.All() {
		if key != pack.Name {
			t.Errorf("map key %q != pack.Name %q", key, pack.Name)
		}
	}
}

// TestISPBaselineContainsCriticalRules ensures the most important ISP
// hygiene rules are present.
func TestISPBaselineContainsCriticalRules(t *testing.T) {
	pack := rulepacks.ISPBaseline
	wantPatterns := []string{
		"isp-baseline: NTP server configured",
		"isp-baseline: SSH enabled for management",
		"isp-baseline: Telnet disabled",
		"isp-baseline: SNMPv1/v2c community strings absent",
	}
	names := map[string]bool{}
	for _, r := range pack.Rules {
		names[r.Name] = true
	}
	for _, want := range wantPatterns {
		if !names[want] {
			t.Errorf("missing rule %q in isp-baseline", want)
		}
	}
}
