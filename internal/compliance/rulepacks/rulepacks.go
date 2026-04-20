// Package rulepacks ships pre-defined compliance rule bundles for common
// ISP/network-operator baseline configurations. Each pack is a named,
// versioned collection of compliance.Rule definitions that can be seeded
// into a tenant's rule set with a single API call.
//
// Pack names follow the pattern "<vendor>-baseline" or "<topic>-baseline".
// All rules are of kind "regex", "must_include", "must_exclude", or
// "ordered_block" — the same kinds supported by the compliance engine.
//
// Security note: packs are read-only definitions in code. Tenants choose
// which packs to apply; applying a pack upserts rules by name so it is
// idempotent and safe to re-apply after a pack version bump.
package rulepacks

import "github.com/i4Edu/netmantle/internal/compliance"

// Pack is a named, versioned collection of compliance rules.
type Pack struct {
	// Name is the unique identifier for this pack (e.g. "isp-baseline").
	Name string
	// Version is a human-readable version string ("1.0", "1.1", …).
	Version string
	// Description explains what the pack checks.
	Description string
	// Rules are the compliance rule templates. TenantID is filled in by
	// the caller when applying the pack to a specific tenant.
	Rules []compliance.Rule
}

// All returns every built-in rule pack keyed by name.
func All() map[string]Pack {
	return map[string]Pack{
		"isp-baseline":        ISPBaseline,
		"cisco-ios-cis":       CiscoIOSCIS,
		"mikrotik-baseline":   MikroTikBaseline,
		"junos-baseline":      JunosBaseline,
		"nokia-sros-baseline": NokiaSROSBaseline,
		"huawei-vrp-baseline": HuaweiVRPBaseline,
	}
}

// Get returns the named pack and whether it was found.
func Get(name string) (Pack, bool) {
	p, ok := All()[name]
	return p, ok
}

// ISPBaseline is a broad baseline pack for ISP/carrier environments.
// It checks common security hygiene across multiple vendors.
var ISPBaseline = Pack{
	Name:        "isp-baseline",
	Version:     "1.0",
	Description: "Broad security baseline for ISP network devices. Checks NTP synchronisation, encrypted password storage, SSH management, SNMP v3 enforcement, logging, and BGP authentication.",
	Rules: []compliance.Rule{
		{
			Name:        "isp-baseline: NTP server configured",
			Kind:        "regex",
			Pattern:     `(?i)ntp\s+server`,
			Severity:    "medium",
			Description: "At least one NTP server should be configured to ensure clock synchronisation.",
		},
		{
			Name:        "isp-baseline: service password-encryption or equivalent",
			Kind:        "regex",
			Pattern:     `(?i)(service\s+password-encryption|password-hash|set\s+system\s+root-authentication|PasswordAuthentication)`,
			Severity:    "high",
			Description: "Passwords must not be stored in plaintext. Enable 'service password-encryption' (Cisco) or equivalent.",
		},
		{
			Name:        "isp-baseline: SSH enabled for management",
			Kind:        "regex",
			Pattern:     `(?i)(ip\s+ssh\s+version\s+2|set\s+system\s+services\s+ssh|sshd\s+service|service\s+ssh)`,
			Severity:    "high",
			Description: "SSH version 2 should be the only enabled remote management protocol.",
		},
		{
			Name:        "isp-baseline: Telnet disabled",
			Kind:        "must_exclude",
			Pattern:     "transport input telnet",
			Severity:    "high",
			Description: "Telnet transmits credentials in cleartext. Disable it on all VTY lines.",
		},
		{
			Name:        "isp-baseline: SNMPv1/v2c community strings absent",
			Kind:        "must_exclude",
			Pattern:     "snmp-server community public",
			Severity:    "critical",
			Description: "The default SNMP community string 'public' must not be present.",
		},
		{
			Name:        "isp-baseline: logging configured",
			Kind:        "regex",
			Pattern:     `(?i)(logging\s+(host|server|trap)|syslog)`,
			Severity:    "medium",
			Description: "Remote syslog should be configured so events are preserved after device reboot.",
		},
		{
			Name:        "isp-baseline: login banner configured",
			Kind:        "regex",
			Pattern:     `(?i)(banner\s+(login|motd|exec)|set\s+system\s+login\s+message)`,
			Severity:    "low",
			Description: "A legal notice / login banner should be displayed to deter unauthorised access.",
		},
		{
			Name:        "isp-baseline: BGP MD5 authentication present when BGP configured",
			Kind:        "regex",
			Pattern:     `(?i)(neighbor\s+\S+\s+password|authentication\s+md5\s+password|bgp\s+authentication-key)`,
			Severity:    "high",
			Description: "BGP sessions should be protected with MD5 or TCP-AO authentication.",
		},
		{
			Name:        "isp-baseline: enable secret set (Cisco)",
			Kind:        "regex",
			Pattern:     `(?i)enable\s+(secret|password\s+\d+)`,
			Severity:    "high",
			Description: "The privileged-mode password must be set using 'enable secret' (hashed), not 'enable password' (reversible).",
		},
	},
}

// CiscoIOSCIS contains CIS Benchmark-inspired rules for Cisco IOS / IOS-XE.
// Reference: CIS Cisco IOS Benchmark v4.x.
var CiscoIOSCIS = Pack{
	Name:        "cisco-ios-cis",
	Version:     "1.0",
	Description: "CIS Benchmark-inspired baseline for Cisco IOS/IOS-XE. Checks AAA, SSH v2, VTY access-class, no CDP on edge interfaces, OSPF/BGP authentication, and NTP auth.",
	Rules: []compliance.Rule{
		{
			Name:        "cisco-ios-cis: AAA new-model enabled",
			Kind:        "must_include",
			Pattern:     "aaa new-model",
			Severity:    "high",
			Description: "AAA new-model must be enabled before configuring authentication/authorisation policies.",
		},
		{
			Name:        "cisco-ios-cis: SSH v2 only",
			Kind:        "must_include",
			Pattern:     "ip ssh version 2",
			Severity:    "high",
			Description: "Only SSH version 2 should be permitted; SSH v1 is cryptographically weak.",
		},
		{
			Name:        "cisco-ios-cis: no ip http server (HTTP disabled)",
			Kind:        "must_exclude",
			Pattern:     "ip http server",
			Severity:    "high",
			Description: "The plain-HTTP management server should be disabled. Use HTTPS only.",
		},
		{
			Name:        "cisco-ios-cis: VTY lines have access-class",
			Kind:        "regex",
			Pattern:     `(?m)line\s+vty[\s\S]*?access-class`,
			Severity:    "high",
			Description: "VTY lines should have an inbound access-class limiting which source IPs can log in.",
		},
		{
			Name:        "cisco-ios-cis: ip tcp intercept or control-plane ACL present",
			Kind:        "regex",
			Pattern:     `(?i)(ip\s+tcp\s+intercept|control-plane|ip\s+access-group\s+\S+\s+in)`,
			Severity:    "medium",
			Description: "Some form of TCP DoS mitigation or CoPP should be configured on management plane.",
		},
		{
			Name:        "cisco-ios-cis: no service finger",
			Kind:        "must_exclude",
			Pattern:     "service finger",
			Severity:    "medium",
			Description: "The finger service exposes user information and should be disabled.",
		},
		{
			Name:        "cisco-ios-cis: no ip source-route",
			Kind:        "must_include",
			Pattern:     "no ip source-route",
			Severity:    "medium",
			Description: "IP source routing should be disabled to prevent route manipulation attacks.",
		},
		{
			Name:        "cisco-ios-cis: service timestamps debug/log datetime",
			Kind:        "regex",
			Pattern:     `(?i)service\s+timestamps\s+(debug|log)\s+datetime`,
			Severity:    "low",
			Description: "Timestamps in log/debug output should include date and time for forensic usefulness.",
		},
	},
}

// MikroTikBaseline contains baseline rules for MikroTik RouterOS devices.
var MikroTikBaseline = Pack{
	Name:        "mikrotik-baseline",
	Version:     "1.0",
	Description: "Security baseline for MikroTik RouterOS. Checks SSH access, Telnet/Winbox disable, SNMP community hygiene, NTP, and firewall presence.",
	Rules: []compliance.Rule{
		{
			Name:        "mikrotik-baseline: SSH enabled",
			Kind:        "must_include",
			Pattern:     "/ip service",
			Severity:    "medium",
			Description: "IP services configuration should be present; ensure SSH is enabled and Telnet disabled.",
		},
		{
			Name:        "mikrotik-baseline: Telnet service disabled",
			Kind:        "must_exclude",
			Pattern:     "name=telnet disabled=no",
			Severity:    "high",
			Description: "Telnet should be explicitly disabled in /ip service.",
		},
		{
			Name:        "mikrotik-baseline: default admin account renamed",
			Kind:        "must_exclude",
			Pattern:     "name=admin",
			Severity:    "high",
			Description: "The default 'admin' account should be renamed or disabled to prevent brute-force attacks.",
		},
		{
			Name:        "mikrotik-baseline: SNMP community not 'public'",
			Kind:        "must_exclude",
			Pattern:     "community=public",
			Severity:    "critical",
			Description: "The default SNMP community string 'public' must be changed.",
		},
		{
			Name:        "mikrotik-baseline: NTP client configured",
			Kind:        "regex",
			Pattern:     `(?i)(ntp\s+client|sntp\s+client)`,
			Severity:    "medium",
			Description: "NTP/SNTP client should be configured for clock synchronisation.",
		},
		{
			Name:        "mikrotik-baseline: firewall filter rules present",
			Kind:        "must_include",
			Pattern:     "/ip firewall filter",
			Severity:    "high",
			Description: "Firewall filter rules should be configured to restrict inbound management access.",
		},
	},
}

// JunosBaseline contains baseline checks for Juniper Junos configuration style.
var JunosBaseline = Pack{
	Name:        "junos-baseline",
	Version:     "1.0",
	Description: "Baseline for Juniper Junos devices. Checks SSH service, root authentication, NTP, SNMP v3 posture, and login banners.",
	Rules: []compliance.Rule{
		{Name: "junos-baseline: SSH service enabled", Kind: "must_include", Pattern: "set system services ssh", Severity: "high", Description: "SSH should be enabled for secure device management."},
		{Name: "junos-baseline: root authentication configured", Kind: "regex", Pattern: `set\s+system\s+root-authentication\s+`, Severity: "critical", Description: "Root authentication must be configured and hashed."},
		{Name: "junos-baseline: NTP server configured", Kind: "regex", Pattern: `set\s+system\s+ntp\s+server`, Severity: "medium", Description: "At least one NTP server should be configured."},
		{Name: "junos-baseline: SNMPv1/v2c community not public", Kind: "must_exclude", Pattern: "set snmp community public", Severity: "critical", Description: "Default public SNMP community should not be present."},
		{Name: "junos-baseline: login banner configured", Kind: "regex", Pattern: `set\s+system\s+login\s+message`, Severity: "low", Description: "A login/legal banner should be configured."},
	},
}

// NokiaSROSBaseline contains baseline checks for Nokia SR OS / TiMOS.
var NokiaSROSBaseline = Pack{
	Name:        "nokia-sros-baseline",
	Version:     "1.0",
	Description: "Baseline for Nokia SR OS. Checks SSH management access, AAA posture, NTP, SNMP community hygiene, and logging.",
	Rules: []compliance.Rule{
		{Name: "nokia-sros-baseline: SSH enabled", Kind: "regex", Pattern: `(?i)ssh`, Severity: "high", Description: "Secure remote management via SSH should be enabled."},
		{Name: "nokia-sros-baseline: AAA profile present", Kind: "regex", Pattern: `(?i)(aaa|radius|tacacs)`, Severity: "medium", Description: "AAA integration should be configured."},
		{Name: "nokia-sros-baseline: NTP configured", Kind: "regex", Pattern: `(?i)\bntp\b`, Severity: "medium", Description: "NTP should be configured for accurate timestamps."},
		{Name: "nokia-sros-baseline: SNMP public community absent", Kind: "must_exclude", Pattern: "community public", Severity: "critical", Description: "SNMP public community should be removed."},
		{Name: "nokia-sros-baseline: remote logging configured", Kind: "regex", Pattern: `(?i)(log-id|syslog)`, Severity: "low", Description: "Remote logging should be configured for operational auditability."},
	},
}

// HuaweiVRPBaseline contains baseline checks for Huawei VRP.
var HuaweiVRPBaseline = Pack{
	Name:        "huawei-vrp-baseline",
	Version:     "1.0",
	Description: "Baseline for Huawei VRP. Checks SSH/STelnet usage, AAA, NTP, SNMP community posture, and info-center logging.",
	Rules: []compliance.Rule{
		{Name: "huawei-vrp-baseline: STelnet/SSH enabled", Kind: "regex", Pattern: `(?i)(stelnet|ssh\s+server\s+enable)`, Severity: "high", Description: "Secure SSH (STelnet) should be enabled."},
		{Name: "huawei-vrp-baseline: Telnet service disabled", Kind: "must_exclude", Pattern: "telnet server enable", Severity: "high", Description: "Telnet should be disabled."},
		{Name: "huawei-vrp-baseline: AAA configured", Kind: "regex", Pattern: `(?i)\baaa\b`, Severity: "medium", Description: "AAA configuration should be present."},
		{Name: "huawei-vrp-baseline: NTP service configured", Kind: "regex", Pattern: `(?i)\bntp-service\b`, Severity: "medium", Description: "NTP service should be configured."},
		{Name: "huawei-vrp-baseline: SNMP public community absent", Kind: "must_exclude", Pattern: "snmp-agent community read public", Severity: "critical", Description: "Default SNMP public community must not be present."},
		{Name: "huawei-vrp-baseline: info-center logging enabled", Kind: "regex", Pattern: `(?i)info-center`, Severity: "low", Description: "Centralized logging should be configured via info-center."},
	},
}
