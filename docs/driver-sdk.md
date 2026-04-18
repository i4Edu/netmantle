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
`init()` into the global `drivers.Registry`. To add a new driver:

1. Create `internal/drivers/builtin/<name>.go` implementing `Driver`.
2. Add a unit test using the `drivers/fakesession` helper.
3. Document any prompt or paging quirks in a comment.
