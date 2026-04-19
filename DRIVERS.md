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

## Scaffolded (registered, backup path not hardened yet)

- `cisco_netconf`
- `junos_netconf`
- `restconf`
- `gnmi`

These stubs are intentionally present for inventory and roadmap visibility, but
currently return "not implemented" style errors from backup execution.

## Planned next vendor additions

Priority sequence for near-term expansion:

1. Fortinet
2. Palo Alto
3. Huawei

Each new driver should be marked explicitly as either **stub** or **hardened**
when merged.

## Driver development references

- Driver interface and conventions:
  [`docs/driver-sdk.md`](docs/driver-sdk.md)
- Builtin implementations:
  `internal/drivers/builtin/builtin.go`
