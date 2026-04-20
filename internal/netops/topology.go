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
	// lldpRow matches "show lldp neighbors" / "show cdp neighbors" output.
	// Format: LocalIntf  ChassisID  RemotePort  RemoteHost  [extras...]
	// Example: Et1   001c.7300.1234   Et2   switch-2
	lldpRow = regexp.MustCompile(
		`^\s*(\S+)\s+\S+\s+(\S+)\s+(\S+)\s*$`)

	// cdpRow matches "show cdp neighbors" compact format:
	// Device ID    Local Intf   Holdtme   Capability  Platform  Port ID
	// switch-2     Gi0/1        120         RS I      WS-C3750  Gi0/0
	cdpRow = regexp.MustCompile(
		`^\s*(\S+)\s+(\S+)\s+\d+\s+\S.*?(\S+)\s*$`)

	// mikrotikNeighborRow matches "/ip neighbor print" output:
	//  0 address=192.168.1.2 identity=switch-2 interface=ether1 ...
	mikrotikNeighborRow = regexp.MustCompile(
		`identity=(\S+).*?interface=(\S+)`)

	headerKeywords = regexp.MustCompile(`(?i)local|chassis|port|system|capab|hold|interface|device.id|platform`)
)

// FromNeighborOutput parses one neighbor-table dump for a device and
// returns the discovered links. It auto-detects LLDP, CDP, and MikroTik
// neighbor output formats.
func FromNeighborOutput(localHost, output string) []Link {
	// MikroTik "/ip neighbor print" output contains "identity=" fields.
	if strings.Contains(output, "identity=") {
		return parseMikrotikNeighbors(localHost, output)
	}
	// CDP output typically includes "Capability" or "Platform" column headers.
	if strings.Contains(strings.ToLower(output), "platform") ||
		strings.Contains(strings.ToLower(output), "holdtme") {
		return parseCDPNeighbors(localHost, output)
	}
	return parseLLDPNeighbors(localHost, output)
}

func parseLLDPNeighbors(localHost, output string) []Link {
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
			B: m[3], BPort: m[2],
		})
	}
	return out
}

// parseCDPNeighbors handles "show cdp neighbors" compact output.
// Columns: Device-ID  Local-Intf  Holdtme  Capability  Platform  Port-ID
func parseCDPNeighbors(localHost, output string) []Link {
	var out []Link
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "-") {
			continue
		}
		if headerKeywords.MatchString(trim) && strings.Count(trim, " ") < 4 {
			continue
		}
		// CDP compact: DeviceID localIntf holdtime capability platform portID
		fields := strings.Fields(trim)
		if len(fields) < 6 {
			continue
		}
		// Skip lines that look like headers (all alpha, no digits in field 2)
		if strings.ContainsAny(fields[2], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
			!strings.ContainsAny(fields[2], "0123456789") {
			continue
		}
		remoteHost := fields[0]
		localPort := fields[1]
		remotePort := fields[len(fields)-1]
		if remoteHost == "" || localPort == "" {
			continue
		}
		out = append(out, Link{
			A: localHost, APort: localPort,
			B: remoteHost, BPort: remotePort,
		})
	}
	return out
}

// parseMikrotikNeighbors handles MikroTik RouterOS "/ip neighbor print" output.
func parseMikrotikNeighbors(localHost, output string) []Link {
	var out []Link
	for _, line := range strings.Split(output, "\n") {
		m := mikrotikNeighborRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		remoteHost := m[1]
		localPort := m[2]
		if remoteHost == "" || localPort == "" || remoteHost == localHost {
			continue
		}
		out = append(out, Link{
			A: localHost, APort: localPort,
			B: remoteHost, BPort: "—",
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
