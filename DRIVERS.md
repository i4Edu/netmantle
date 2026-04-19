# DRIVERS

This file tracks builtin driver maturity so reviewers can quickly see hardened
coverage vs scaffolded transports.

## Hardened (CLI backup path implemented)

- `cisco_ios`
- `cisco_nxos`
- `cisco_iosxr`
- `arista_eos`
- `junos_cli`
- `mikrotik_routeros`
- `nokia_sros`
- `bdcom_os`
- `vsol_os`
- `dbc_os`
- `generic_ssh` (best-effort fallback)
- `fortios` — Fortinet FortiOS; disables pager via `config system console`, dumps via `show full-configuration`
- `paloalto_panos` — Palo Alto Networks PAN-OS; disables pager via `set cli pager off`, dumps via `show config running`
- `huawei_vrp` — Huawei VRP; suppresses pager with `screen-length 0 temporary`, dumps via `display current-configuration`
- `cisco_netconf` — Cisco IOS-XE/NX-OS via NETCONF-over-SSH (RFC 6241/6242); retrieves running datastore as `netconf-config` artifact. Requires `backup.Service.NetconfSession` factory.
- `junos_netconf` — Juniper Junos via NETCONF-over-SSH; retrieves both running and candidate datastores. Requires `backup.Service.NetconfSession` factory.
- `restconf` — model-driven backup via `get-config:running` over the hardened NetconfSession path; stores `restconf-running`.
- `gnmi` — model-driven backup via `get-config:running` over the hardened NetconfSession path; stores `gnmi-running`.

## Scaffolded (registered, backup path not hardened yet)

No scaffolded builtin drivers at this time.

## Driver development references

- Driver interface and conventions:
  [`docs/driver-sdk.md`](docs/driver-sdk.md)
- Builtin implementations:
  `internal/drivers/builtin/builtin.go`
- NETCONF transport:
  `internal/transport/netconf.go` + `internal/drivers/netconf/`
