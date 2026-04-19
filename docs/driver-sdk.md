# Driver SDK (Phase 1)

A driver teaches NetMantle how to talk to a device family. The interface is
small on purpose so that adding a new platform is a few hundred lines of Go.

```go
type Driver interface {
    // Name uniquely identifies the driver (e.g. "cisco_ios").
    Name() string

    // FetchConfig opens a session via the supplied dialer, runs the
    // platform-specific commands, and returns one or more configuration
    // artifacts (typically "running-config" and optionally "startup-config").
    FetchConfig(ctx context.Context, sess Session) ([]ConfigArtifact, error)
}

type ConfigArtifact struct {
    Name    string // e.g. "running-config"
    Content []byte
}

type Session interface {
    // Run a single command, returning the captured output with the prompt
    // and pager artefacts already stripped.
    Run(ctx context.Context, cmd string) (string, error)
}
```

The `transport` package provides `Session` implementations (Phase 1: SSH).
A driver should:

1. Send any "disable paging" command(s) the platform requires.
2. Issue the configuration-show command.
3. Strip banners / timestamps that change between runs (where reasonable;
   diff-ignore rules will live in `internal/diff` in Phase 2).
4. Return a `[]ConfigArtifact` — never log secrets.

Builtin drivers live under `internal/drivers/builtin/` and self-register via
`init()` using the package-level `drivers.Register` API. To add a new driver:

1. Create `internal/drivers/builtin/<name>.go` implementing `Driver`.
2. Add a unit test using the `drivers/fakesession` helper.
3. Document any prompt or paging quirks in a comment.

## Builtin drivers

The following platforms ship in `internal/drivers/builtin` and are registered
under these names:

| Driver name        | Platform                                | Notes                                        |
|--------------------|-----------------------------------------|----------------------------------------------|
| `cisco_ios`        | Cisco IOS / IOS-XE                      | `terminal length 0`; running + optional startup |
| `cisco_nxos`       | Cisco Nexus (NX-OS)                     | `terminal length 0`; running + optional startup |
| `cisco_iosxr`      | Cisco IOS-XR                            | `terminal length 0`; running-config            |
| `arista_eos`       | Arista EOS                              | `terminal length 0`; running-config            |
| `junos_cli`        | Juniper Junos (CLI)                     | `set cli screen-length 0`; `show configuration | display set` |
| `mikrotik_routeros`| MikroTik RouterOS                       | `/export`                                    |
| `nokia_sros`       | Nokia SR OS / TiMOS                     | `environment no more`; `admin display-config`   |
| `bdcom_os`         | BDCOM OLT / switch                      | Cisco-style CLI                              |
| `vsol_os`          | V-SOL OLT                               | `enable`; Cisco-style CLI                    |
| `dbc_os`           | DBC OLT                                 | `enable`; Cisco-style CLI                    |
| `generic_ssh`      | Fallback for unknown platforms          | tries `show configuration` then `show running-config` |
| `cisco_netconf`, `junos_netconf`, `restconf`, `gnmi` | Phase 10 modern transports | Registered as stubs; backup wiring is roadmap work |
