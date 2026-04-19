// Package netops provides cross-cutting network-operations helpers that
// don't belong to a single phase package — currently the topology builder
// (Phase 10).
//
// Topology() parses LLDP / CDP neighbor tables out of the latest stored
// configurations or probe outputs and returns a graph of links between
// hostnames. It is intentionally text-based so it works against any device
// that exposes neighbor info via a CLI command.
package netops

import (
	"regexp"
	"sort"
	"strings"
)

// Link is one end-to-end neighbor edge.
type Link struct {
	A     string `json:"a"`
	APort string `json:"a_port"`
	B     string `json:"b"`
	BPort string `json:"b_port"`
}

// Graph is a deduplicated set of Links with stable versioning metadata.
//
// APIVersion is included so consumers can detect when the schema has
// evolved. It follows semver; breaking changes bump the major component.
type Graph struct {
	APIVersion string `json:"api_version"`
	NodeCount  int    `json:"node_count"`
	EdgeCount  int    `json:"edge_count"`
	Links      []Link `json:"links"`
}

// currentAPIVersion is the graph schema version. Increment the major
// component if the shape of Graph, Link, or the endpoint contract changes
// in a breaking way.
const currentAPIVersion = "1.0"

var (
	// "show lldp neighbors" / "show cdp neighbors" common output formats:
	//   Local Intf      Chassis ID         Port ID         System Name
	//   Et1             001c.7300.1234     Et2             switch-2
	lldpRow = regexp.MustCompile(
		`^\s*(\S+)\s+\S+\s+(\S+)\s+(\S+)\s*$`)
	headerKeywords = regexp.MustCompile(`(?i)local|chassis|port|system|capab|hold|interface`)
)

// FromNeighborOutput parses one neighbor-table dump for a device and
// returns the discovered links. Lines that look like headers, separators,
// or don't match are skipped.
func FromNeighborOutput(localHost, output string) []Link {
	var out []Link
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "Capability") {
			continue
		}
		if headerKeywords.MatchString(trim) && strings.Count(trim, " ") < 6 {
			continue
		}
		m := lldpRow.FindStringSubmatch(trim)
		if m == nil {
			continue
		}
		out = append(out, Link{
			A: localHost, APort: m[1],
			B: m[3], BPort: m[2], // remote port appears in field 2 in the regex
		})
	}
	return out
}

// Merge combines per-device link lists, deduplicating undirected edges.
// The returned Graph has APIVersion, NodeCount, and EdgeCount populated.
func Merge(perDevice map[string][]Link) Graph {
	seen := map[string]Link{}
	nodes := map[string]struct{}{}
	for _, links := range perDevice {
		for _, l := range links {
			a, b := l.A+"|"+l.APort, l.B+"|"+l.BPort
			key := a + "::" + b
			rev := b + "::" + a
			if _, ok := seen[rev]; ok {
				continue
			}
			seen[key] = l
			nodes[l.A] = struct{}{}
			nodes[l.B] = struct{}{}
		}
	}
	out := make([]Link, 0, len(seen))
	for _, l := range seen {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].APort < out[j].APort
	})
	return Graph{
		APIVersion: currentAPIVersion,
		NodeCount:  len(nodes),
		EdgeCount:  len(out),
		Links:      out,
	}
}
