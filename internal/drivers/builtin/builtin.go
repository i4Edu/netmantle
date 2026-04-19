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
	drivers.Register(&ciscoNXOS{})
	drivers.Register(&ciscoIOSXR{})
	drivers.Register(&aristaEOS{})
	drivers.Register(&junosCLI{})
	drivers.Register(&mikrotikROS{})
	drivers.Register(&nokiaSROS{})
	drivers.Register(&bdcomOS{})
	drivers.Register(&vsolOS{})
	drivers.Register(&dbcOS{})
	drivers.Register(&genericSSH{})
	// New vendor drivers (hardened CLI backup path).
	drivers.Register(&fortiosDriver{})
	drivers.Register(&paloaltoPANOS{})
	drivers.Register(&huaweiVRP{})
	// NETCONF drivers (hardened — requires NetconfSession factory in backup.Service).
	drivers.Register(&ciscoNetconf{})
	drivers.Register(&junosNetconf{})
	drivers.Register(&restconfDriver{})
	drivers.Register(&gnmiDriver{})
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

type restconfDriver struct{}

func (restconfDriver) Name() string { return "restconf" }

func (restconfDriver) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	cfg, err := s.Run(ctx, "get-config:running")
	if err != nil {
		return nil, fmt.Errorf("restconf: get-config: %w", err)
	}
	if cfg == "" {
		return nil, fmt.Errorf("restconf: empty config returned")
	}
	return []drivers.ConfigArtifact{
		{Name: "restconf-config", Content: []byte(strings.TrimRight(cfg, "\n") + "\n")},
	}, nil
}

type gnmiDriver struct{}

func (gnmiDriver) Name() string { return "gnmi" }

func (gnmiDriver) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	cfg, err := s.Run(ctx, "get-config:running")
	if err != nil {
		return nil, fmt.Errorf("gnmi: get-config: %w", err)
	}
	if cfg == "" {
		return nil, fmt.Errorf("gnmi: empty config returned")
	}
	return []drivers.ConfigArtifact{
		{Name: "gnmi-config", Content: []byte(strings.TrimRight(cfg, "\n") + "\n")},
	}, nil
}

// ciscoNetconf implements NETCONF-based backup for Cisco IOS-XE / NX-OS
// devices that support RFC 6241 NETCONF. The backup service routes these
// devices through transport.DialNetconf (SSH subsystem mode) which exposes
// sess.Run("get-config:running"). The raw <data> XML is stored as the
// "netconf-config" artifact.
type ciscoNetconf struct{}

func (ciscoNetconf) Name() string { return "cisco_netconf" }

func (ciscoNetconf) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	cfg, err := s.Run(ctx, "get-config:running")
	if err != nil {
		return nil, fmt.Errorf("cisco_netconf: get-config: %w", err)
	}
	if cfg == "" {
		return nil, fmt.Errorf("cisco_netconf: empty config returned")
	}
	return []drivers.ConfigArtifact{
		{Name: "netconf-config", Content: []byte(strings.TrimRight(cfg, "\n") + "\n")},
	}, nil
}

// junosNetconf implements NETCONF-based backup for Juniper Junos devices.
// Junos has native NETCONF support (RFC 6241) and exposes both the running
// and candidate datastores; we capture both when available.
type junosNetconf struct{}

func (junosNetconf) Name() string { return "junos_netconf" }

func (junosNetconf) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	running, err := s.Run(ctx, "get-config:running")
	if err != nil {
		return nil, fmt.Errorf("junos_netconf: get-config running: %w", err)
	}
	arts := []drivers.ConfigArtifact{
		{Name: "netconf-running", Content: []byte(strings.TrimRight(running, "\n") + "\n")},
	}
	// Candidate datastore is optional; not all Junos versions expose it
	// over NETCONF in read mode. Ignore the error if unavailable.
	if candidate, err := s.Run(ctx, "get-config:candidate"); err == nil && candidate != "" {
		arts = append(arts, drivers.ConfigArtifact{
			Name: "netconf-candidate", Content: []byte(strings.TrimRight(candidate, "\n") + "\n"),
		})
	}
	return arts, nil
}

// ciscoNXOS implements Cisco Nexus (NX-OS) backup. NX-OS shares Cisco's
// "terminal length 0" paging knob with IOS but ships a distinct CLI.
type ciscoNXOS struct{}

func (ciscoNXOS) Name() string { return "cisco_nxos" }

func (ciscoNXOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("cisco_nxos: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("cisco_nxos: show running-config: %w", err)
	}
	out := []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(stripIOSChrome(running))},
	}
	if startup, err := s.Run(ctx, "show startup-config"); err == nil {
		out = append(out, drivers.ConfigArtifact{
			Name: "startup-config", Content: []byte(stripIOSChrome(startup)),
		})
	}
	return out, nil
}

// ciscoIOSXR implements Cisco IOS-XR backup. IOS-XR uses
// "terminal length 0" plus the explicit `show running-config` form.
type ciscoIOSXR struct{}

func (ciscoIOSXR) Name() string { return "cisco_iosxr" }

func (ciscoIOSXR) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("cisco_iosxr: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("cisco_iosxr: show running-config: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(stripIOSChrome(running))},
	}, nil
}

// nokiaSROS implements Nokia SR OS / TiMOS classic CLI backup. Paging is
// disabled with "environment no more"; the full config is dumped via
// "admin display-config".
type nokiaSROS struct{}

func (nokiaSROS) Name() string { return "nokia_sros" }

func (nokiaSROS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	if _, err := s.Run(ctx, "environment no more"); err != nil {
		return nil, fmt.Errorf("nokia_sros: environment no more: %w", err)
	}
	out, err := s.Run(ctx, "admin display-config")
	if err != nil {
		return nil, fmt.Errorf("nokia_sros: admin display-config: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}

// bdcomOS implements BDCOM (BroaDBand Communications) OLT/switch backup.
// BDCOM CLI is Cisco-flavoured: paging is disabled via "terminal length 0"
// and the full configuration is dumped with "show running-config".
type bdcomOS struct{}

func (bdcomOS) Name() string { return "bdcom_os" }

func (bdcomOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("bdcom_os: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("bdcom_os: show running-config: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(stripIOSChrome(running))},
	}, nil
}

// vsolOS implements V-SOL OLT backup. V-SOL devices expose a Cisco-style
// CLI; "enable" is required for full config visibility on most models.
type vsolOS struct{}

func (vsolOS) Name() string { return "vsol_os" }

func (vsolOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	// "enable" may be a no-op when the user is already privileged; ignore
	// failure rather than refusing to back the device up.
	_, _ = s.Run(ctx, "enable")
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("vsol_os: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("vsol_os: show running-config: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(stripIOSChrome(running))},
	}, nil
}

// dbcOS implements DBC (DigitalBroadband Communications) OLT backup.
// DBC OLTs use a Cisco-style CLI similar to V-SOL/BDCOM; paging is
// disabled with "terminal length 0".
type dbcOS struct{}

func (dbcOS) Name() string { return "dbc_os" }

func (dbcOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	_, _ = s.Run(ctx, "enable")
	if _, err := s.Run(ctx, "terminal length 0"); err != nil {
		return nil, fmt.Errorf("dbc_os: terminal length: %w", err)
	}
	running, err := s.Run(ctx, "show running-config")
	if err != nil {
		return nil, fmt.Errorf("dbc_os: show running-config: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(stripIOSChrome(running))},
	}, nil
}

// fortiosDriver implements Fortinet FortiOS backup via `show full-configuration`.
// The CLI pager is suppressed via `config system console` before the main
// command so that long configs are not truncated by --More-- prompts.
type fortiosDriver struct{}

func (fortiosDriver) Name() string { return "fortios" }

func (fortiosDriver) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	// Disable the terminal pager for the session. Non-fatal: some read-only
	// accounts or locked-down versions do not allow console reconfiguration.
	_, _ = s.Run(ctx, "config system console")
	_, _ = s.Run(ctx, "set output standard")
	_, _ = s.Run(ctx, "end")

	out, err := s.Run(ctx, "show full-configuration")
	if err != nil {
		return nil, fmt.Errorf("fortios: show full-configuration: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}

// paloaltoPANOS implements Palo Alto Networks PAN-OS backup.
// Paging is turned off with `set cli pager off` before the main dump.
type paloaltoPANOS struct{}

func (paloaltoPANOS) Name() string { return "paloalto_panos" }

func (paloaltoPANOS) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	// Turn off the CLI pager so long configurations are not interrupted.
	if _, err := s.Run(ctx, "set cli pager off"); err != nil {
		return nil, fmt.Errorf("paloalto_panos: set cli pager off: %w", err)
	}
	out, err := s.Run(ctx, "show config running")
	if err != nil {
		return nil, fmt.Errorf("paloalto_panos: show config running: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}

// huaweiVRP implements Huawei VRP (Versatile Routing Platform) backup.
// Paging is disabled temporarily with `screen-length 0 temporary`; the
// full running configuration is captured via `display current-configuration`.
type huaweiVRP struct{}

func (huaweiVRP) Name() string { return "huawei_vrp" }

func (huaweiVRP) FetchConfig(ctx context.Context, s drivers.Session) ([]drivers.ConfigArtifact, error) {
	// Disable paging for this session only (does not persist across logins).
	if _, err := s.Run(ctx, "screen-length 0 temporary"); err != nil {
		return nil, fmt.Errorf("huawei_vrp: screen-length: %w", err)
	}
	out, err := s.Run(ctx, "display current-configuration")
	if err != nil {
		return nil, fmt.Errorf("huawei_vrp: display current-configuration: %w", err)
	}
	return []drivers.ConfigArtifact{
		{Name: "running-config", Content: []byte(strings.TrimRight(out, "\n") + "\n")},
	}, nil
}
