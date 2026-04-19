// Package diff produces unified diffs between two textual configuration
// versions, with platform-aware "ignore" rules so volatile lines (uptime
// counters, timestamps, MOTD, etc.) don't pollute change history.
package diff

import (
	"regexp"
	"strings"
)

// Result describes the difference between two versions.
type Result struct {
	Unified      string
	AddedLines   int
	RemovedLines int
	Identical    bool // true if, after ignore-rules, the two versions match
}

// IgnoreRule excludes lines matching Pattern from comparison. Rules are
// applied per artifact name (e.g. only to "running-config").
type IgnoreRule struct {
	ArtifactPrefix string         // empty matches all artifacts
	Pattern        *regexp.Regexp // line removed before diffing
	Description    string
}

// Engine computes diffs subject to a set of ignore rules.
type Engine struct {
	Rules []IgnoreRule
}

// DefaultRules returns ignore rules for the platforms shipped in Phase 1.
// Each rule is broad enough to cover the noisiest fields; users can layer
// their own through the API in a follow-up.
func DefaultRules() []IgnoreRule {
	mk := func(p, desc string) IgnoreRule {
		return IgnoreRule{Pattern: regexp.MustCompile(p), Description: desc}
	}
	return []IgnoreRule{
		mk(`^!\s*Last configuration change`, "Cisco: last config change timestamp"),
		mk(`^!\s*NVRAM config last updated`, "Cisco: NVRAM update timestamp"),
		mk(`^!\s*Time:`, "Generic timestamp comment"),
		mk(`^Building configuration\.\.\.`, "Cisco: build-config banner"),
		mk(`^Current configuration : \d+ bytes`, "Cisco: current-config size"),
		mk(`^!\s*No configuration change since last restart`, "Cisco: restart marker"),
		mk(`^# Last commit:`, "Junos: last commit timestamp"),
	}
}

// Diff produces a Result for two textual artifacts.
func (e *Engine) Diff(artifact, oldText, newText string) Result {
	o := e.filter(artifact, oldText)
	n := e.filter(artifact, newText)
	if o == n {
		return Result{Identical: true}
	}
	return unified(o, n)
}

func (e *Engine) filter(artifact, text string) string {
	if len(e.Rules) == 0 {
		return text
	}
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		drop := false
		for _, r := range e.Rules {
			if r.ArtifactPrefix != "" && !strings.HasPrefix(artifact, r.ArtifactPrefix) {
				continue
			}
			if r.Pattern.MatchString(line) {
				drop = true
				break
			}
		}
		if !drop {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// unified produces a small unified-style diff using the LCS algorithm. We
// avoid pulling a diff library to keep dependencies tight; this
// implementation is fine for typical config sizes (single-digit thousands
// of lines).
func unified(a, b string) Result {
	aLines := strings.Split(strings.TrimRight(a, "\n"), "\n")
	bLines := strings.Split(strings.TrimRight(b, "\n"), "\n")

	m, n := len(aLines), len(bLines)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if aLines[i] == bLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out strings.Builder
	added, removed := 0, 0
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case aLines[i] == bLines[j]:
			out.WriteString("  ")
			out.WriteString(aLines[i])
			out.WriteByte('\n')
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out.WriteString("- ")
			out.WriteString(aLines[i])
			out.WriteByte('\n')
			removed++
			i++
		default:
			out.WriteString("+ ")
			out.WriteString(bLines[j])
			out.WriteByte('\n')
			added++
			j++
		}
	}
	for ; i < m; i++ {
		out.WriteString("- ")
		out.WriteString(aLines[i])
		out.WriteByte('\n')
		removed++
	}
	for ; j < n; j++ {
		out.WriteString("+ ")
		out.WriteString(bLines[j])
		out.WriteByte('\n')
		added++
	}
	return Result{Unified: out.String(), AddedLines: added, RemovedLines: removed}
}
