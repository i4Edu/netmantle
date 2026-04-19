# Contributing to NetMantle

Thanks for your interest in contributing! NetMantle is an open, vendor-agnostic
network configuration management and automation platform.

## Development setup

Requirements:

- Go 1.25 or newer
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

See [docs/architecture.md](docs/architecture.md) and [ARCHITECTURE.md](ARCHITECTURE.md)
for the high-level design and the [`/internal`](internal) tree for module
boundaries. Cross-cutting decisions live as ADRs under [`docs/adr/`](docs/adr).

## Adding a device driver

Drivers implement `internal/drivers.Driver`. See
[docs/driver-sdk.md](docs/driver-sdk.md) for the contract and an example.
Keep [DRIVERS.md](DRIVERS.md) updated with the new driver's **stub** or
**hardened** status when merging.

## Schema migrations

Migration files live under `internal/storage/migrations/` and are embedded in
the binary. When adding a migration:

1. Use the next sequential four-digit prefix (e.g. `0003_my_change.sql`).
2. Never edit or delete a migration that has already been released.
3. Include compatibility notes in your PR description: which existing data is
   affected and what the rollback procedure is.
4. Add or update tests that exercise the new schema in the same PR.

## Reporting security issues

Please do **not** open public issues for security vulnerabilities. Report them
privately via GitHub Security Advisories (see [SECURITY.md](SECURITY.md) for
the full policy and advisory link).

## Pull requests

- Keep PRs focused; open a discussion first for large changes.
- Add unit tests for new behaviour.
- Run `make lint test` before pushing.
- Update docs and OpenAPI when API behaviour changes.
- For endpoints marked `x-stability: frozen` in
  `internal/api/openapi/openapi.yaml`, breaking changes require a new major
  API version and a discussion with maintainers first.
