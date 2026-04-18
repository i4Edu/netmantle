# Contributing to NetMantle

Thanks for your interest in contributing! NetMantle is an open, vendor-agnostic
network configuration management and automation platform.

## Development setup

Requirements:

- Go 1.22 or newer
- `make`
- (optional) `docker` for container builds

Common tasks:

```bash
make deps     # download Go modules
make lint     # gofmt + go vet
make test     # unit tests
make build    # build the netmantle binary into ./bin/
make run      # run with a local SQLite database
make docker   # build the container image
```

## Project layout

See [docs/architecture.md](docs/architecture.md) for the high-level design and
the [`/internal`](internal) tree for module boundaries. Cross-cutting
decisions live as ADRs under [`docs/adr/`](docs/adr).

## Adding a device driver

Drivers implement `internal/drivers.Driver`. See
[docs/driver-sdk.md](docs/driver-sdk.md) for the contract and an example.

## Reporting security issues

Please do **not** open public issues for security vulnerabilities. Email the
maintainers privately so we can coordinate a fix and disclosure.

## Pull requests

- Keep PRs focused; open a discussion first for large changes.
- Add unit tests for new behaviour.
- Run `make lint test` before pushing.
- Update docs and OpenAPI when API behaviour changes.
