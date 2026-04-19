// Package builtin registers the device drivers shipped with NetMantle.
//
// Importing this package as a side-effect import (`_ "..."`) ensures all
// builtin drivers are present in the global registry.
package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/i4Edu/netmantle/internal/drivers"
)

func init() {
	drivers.Register(&ciscoIOS{})
	drivers.Register(&aristaEOS{})
	drivers.Register(&junosCLI{})
	drivers.Register(&mikrotikROS{})
	drivers.Register(&genericSSH{})
	drivers.Register(&netconfStub{name: "cisco_netconf"})
	drivers.Register(&netconfStub{name: "junos_netconf"})
	drivers.Register(&netconfStub{name: "restconf"})
	drivers.Register(&netconfStub{name: "gnmi"})
}

// ciscoIOS implements the Cisco IOS / IOS-XE backup flow.
type ciscoIOS struct{}

func (ciscoIOS) Name() string { return "cisco_ios" }

func (ciscoIOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	// Disable the more-prompt for the rest of the session.
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("cisco_ios: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("cisco_ios: show running-config: %w", err)
	}
	startup, err := s.Run(ctx, "show startup-config")
	if err != nil {
		// Not all platforms expose startup-config (e.g. some virtual
		// devices); treat as warning by returning what we have.
		return []drivers.ConfigArtifact{{Name: "running-config", Content: []byte(stripIOSChrome(running))}}, nil
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(stripIOSChrome(running))},
		{Name: "startup-config", Content: []byte(stripIOSChrome(startup))},
	}, nil
}

// stripIOSChrome removes IOS lines that change every backup but carry no
// configuration intent (e.g. "Building configuration...", "Current
// configuration : N bytes"). Phase 2's diff/ignore engine will subsume
// this; for now we keep the captured artefact stable across runs.
func stripIOSChrome(raw string) string {
	var out strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Building configuration"):
			continue
		case strings.HasPrefix(t, "Current configuration "):
			continue
		case strings.HasPrefix(t, "Last configuration change"):
			continue
		case strings.HasPrefix(t, "! Last configuration change"):
			continue
		case strings.HasPrefix(t, "! NVRAM config last updated"):
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

// aristaEOS implements the Arista EOS backup flow.
type aristaEOS struct{}

func (aristaEOS) Name() string { return "arista_eos" }

func (aristaEOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("arista_eos: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("arista_eos: show running-config: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(strings.TrimRight(running, "\n") + "\n")},
	}, nil
}

// genericSSH is a fallback driver that simply runs a single user-friendly
// "show" command. It exists so operators can add a device of an unknown
// platform and still get *something* into the config store while a proper
// driver is being written.
type genericSSH struct{}

func (genericSSH) Name() string { return "generic_ssh" }

func (genericSSH) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	out, err := s.Run(ctx, "show configuration")
	if err != nil {
		// Try a Cisco-style fallback before giving up.
		out, err = s.Run(ctx, "show running-config")
		if err != nil {
			return nil, fmt.Errorf("generic_ssh: no usable show command: %w", err)
		}
	}
	return []drivers.ConfigArtifact{
		{Name: "configuration", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}

// junosCLI implements a minimal Juniper Junos backup flow over CLI. The
// recommended NETCONF path is exposed as the junos_netconf stub driver
// (see netconfStub below).
type junosCLI struct{}

func (junosCLI) Name() string { return "junos_cli" }

func (junosCLI) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	if _, err := s.Run(ctx, "set cli screen-length 0"); err != nil {
		return nil, fmt.Errorf("junos_cli: screen-length: %w", err)
	}
	out, err := s.Run(ctx, "show configuration | display set | no-more")
	if err != nil {
		return nil, fmt.Errorf("junos_cli: show configuration: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "configuration", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}

// mikrotikROS implements MikroTik RouterOS backup via /export.
type mikrotikROS struct{}

func (mikrotikROS) Name() string { return "mikrotik_routeros" }

func (mikrotikROS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	out, err := s.Run(ctx, "/export")
	if err != nil {
		return nil, fmt.Errorf("mikrotik_routeros: /export: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "export", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}

// netconfStub is a placeholder for the Phase 10 NETCONF/RESTCONF/gNMI
// drivers. It registers under the requested name so devices can be
// inventoried, and returns a clear "not implemented" error from
// FetchConfig pointing operators at the roadmap.
type netconfStub struct{ name string }

func (n *netconfStub) Name() string { return n.name }

func (n *netconfStub) FetchConfig(ctx context.Context, _ drivers.Session) ([]drivers.ConfigArtifact, error) {
	return nil, fmt.Errorf("%s: NETCONF/RESTCONF/gNMI driver is scaffolded; backup wiring lands in a follow-up PR", n.name)
}
