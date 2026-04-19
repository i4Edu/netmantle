# Copilot / AI contributor instructions for NetMantle

This file is the single source of truth for AI assistants (GitHub Copilot
chat / coding agent, and other LLM‑based contributors) working in this
repository. Human contributors should read [`CONTRIBUTING.md`](../CONTRIBUTING.md)
first — this document layers extra rules on top of it that exist specifically
to keep AI‑generated changes safe and reviewable.

A copy of these rules also lives at [`AGENTS.md`](../AGENTS.md) for tools that
look there by convention. Keep the two files in sync.

---

## 1. Mental model of the project

NetMantle is a **modular monolith in Go 1.25+**. One binary (`cmd/netmantle`)
hosts the REST API, embedded web UI, scheduler, backup workers, notifier,
search indexer, compliance evaluator, terminal, GitOps mirror, and poller
registry. Persistence is **SQLite‑first** with append‑only migrations.
Configuration history for every device is stored in a **per‑device git
repository** under `storage.config_repo_root`.

Read these before making non‑trivial changes:

- [`README.md`](../README.md)
- [`ARCHITECTURE.md`](../ARCHITECTURE.md) and [`docs/architecture.md`](../docs/architecture.md)
- [`docs/roadmap.md`](../docs/roadmap.md) — the *shipped vs scaffolded* table
- [`DRIVERS.md`](../DRIVERS.md) — *hardened vs stub* per driver
- [`SECURITY.md`](../SECURITY.md) and [`docs/threat-model.md`](../docs/threat-model.md)

## 2. Repository layout

```
cmd/netmantle/                 main.go: wires every service together
internal/
  api/                         HTTP server, handlers, OpenAPI spec
  apitokens/                   API token auth
  audit/                       Audit log writer
  auth/                        Sessions, RBAC, bootstrap admin
  automation/                  Push-job orchestration (preview today)
  backup/                      Backup orchestration + PostCommit hooks
  changereq/                   Change-request workflow
  changes/                     Diff retrieval and change_events CRUD
  compliance/                  Rules / rulesets / findings
  config/                      YAML + NETMANTLE_* env loading
  configstore/                 Per-device git repo wrapper
  credentials/                 Envelope-encrypted credential store
  crypto/                      Sealer / KEK derivation
  devices/                     Inventory CRUD + device groups
  diff/                        Diff engine + default rules
  discovery/                   TCP/banner scan, NetBox import
  drivers/                     Driver interface + builtin/ implementations
  gitops/                      Mirror push to external git remotes
  integration/                 NetBox / external integrations
  logging/                     slog setup
  netops/                      Network operations helpers
  notify/                      Webhook / Slack / email channels
  observability/               Prometheus metrics
  poller/                      Distributed poller registration / heartbeat
  probes/                      Runtime probes framework
  scheduler/                   Leader-elected job runner (DB lease)
  search/                      SQLite FTS5 indexing + saved searches
  storage/                     DB open + migrations (embedded SQL)
  storage/migrations/          0001_*.sql ... append-only
  tenants/                     Tenant CRUD + quotas
  terminal/                    Web terminal backend (SSH today)
  transport/                   SSH transport (DialSSH, DialSSHShell)
  version/                     ldflags-injected version string
  web/                         Embedded UI (static/ + Go embed)
deploy/helm/netmantle/         Helm chart
docs/                          Reviewer + operator documentation
docs/adr/                      Architecture Decision Records
.github/workflows/             CI (build/test/lint) + release (sign + SBOM)
```

## 3. Build, lint, test commands

These are the **only** commands you should rely on. Do not invent new
tooling unless the user asks.

```bash
make deps     # go mod download
make tidy     # go mod tidy (CI fails if go.mod/go.sum drift)
make fmt      # gofmt -s -w on all .go files
make vet      # go vet ./...
make lint     # gofmt check + go vet  (this is what CI runs)
make test     # go test -race -count=1 ./...
make cover    # test + coverage summary
make build    # produces ./bin/netmantle
make run      # build + serve with config.example.yaml
make docker   # docker build -t netmantle:<version> .
```

CI ([`.github/workflows/ci.yml`](workflows/ci.yml)) runs `go mod tidy` (and
fails on drift), `make lint`, `make test`, and `make build`. Run the same
locally before declaring work complete.

## 4. Coding conventions (observed in the existing Go code)

- **Package layout**: one concept per package under `internal/`, names match
  the directory.
- **Constructors**: `NewService`, `NewRepo`, `New(...)` returning a pointer.
- **Errors**: return `error`; wrap with `fmt.Errorf("...: %w", err)`. Never
  `panic` in request paths.
- **Logging**: use the `*slog.Logger` injected at construction; structured
  key/value pairs only. Do not introduce `fmt.Println` or `log` package usage.
- **Tenancy**: every query touching tenant data takes a `tenantID int64`
  parameter and includes `tenant_id = ?` in SQL. Do not regress this.
- **Migrations**: append‑only under `internal/storage/migrations/` using the
  next four‑digit prefix. Never edit a released migration.
- **OpenAPI**: API changes update `internal/api/openapi/openapi.yaml`.
  Endpoints with `x-stability: frozen` require a new major API version for
  breaking changes — do not break them.
- **Tests**: prefer table‑driven tests with `t.Run`. Use `t.Setenv` for env
  overrides. Tests must pass under `-race`.
- **Formatting**: `gofmt -s` is enforced by `make lint`. Run `make fmt` after
  any edit you make.

## 5. The "shipped vs scaffolded" rule (read this twice)

The roadmap deliberately distinguishes **shipped** code (production‑intended,
exercised by tests) from **scaffolded** code (registered for inventory or to
preserve API shape, but returning a clear "not implemented" / preview‑only
error). This honesty is a feature.

When working in scaffolded areas (currently: full NETCONF/RESTCONF/gNMI
backup wiring, gRPC poller wire protocol, per‑driver `Apply()` execution in
`internal/automation`, topology graph‑canvas renderer, additional vendor
drivers beyond those listed as **hardened** in [`DRIVERS.md`](../DRIVERS.md),
HA failover validation):

- **Do not** silently replace the stub with a fake/incomplete implementation
  that hides the limitation. The stub error message is intentional UX.
- **Do not** delete a stub to "clean up" — the registration preserves the
  surface for the real implementation.
- If asked to actually implement one of these areas, scope it to a focused PR,
  add real tests, update [`DRIVERS.md`](../DRIVERS.md) / [`docs/roadmap.md`](../docs/roadmap.md)
  to move the item from *follow‑up* to *shipped*, and call out any remaining
  caveats explicitly.

## 6. Adding a driver

1. Implement the `Driver` interface (see [`docs/driver-sdk.md`](../docs/driver-sdk.md)
   and existing implementations in `internal/drivers/builtin/`).
2. Register it in `internal/drivers/builtin/builtin.go`.
3. Add unit tests covering pager suppression, banner handling, and config
   capture.
4. Update [`DRIVERS.md`](../DRIVERS.md) — list it under **Hardened** only when
   the CLI backup path is genuinely exercised; otherwise list it under
   **Scaffolded**.

## 7. Adding a schema migration

1. Add `internal/storage/migrations/000N_short_name.sql` using the next
   sequential prefix.
2. Never edit or delete a released migration.
3. Cover the new schema with tests in the same PR.
4. Mention rollback expectations in the PR description.

## 8. Security guardrails

- Never log secrets, master passphrases, session keys, credential plaintext,
  or webhook tokens. Existing code uses `credRepo.Use(...)` to scope
  cleartext to the dial call — preserve that pattern.
- Never store new long‑lived secrets unencrypted. Use `internal/crypto`
  envelope encryption (see how `credentials` and `gitops_mirrors.secret_envelope`
  do it).
- Never weaken or remove the `tenant_id = ?` predicate from a query.
- Never disable RBAC checks in `internal/api` handlers.
- If a change might affect threat surfaces in [`docs/threat-model.md`](../docs/threat-model.md),
  update that doc in the same PR.

## 9. Documentation expectations

- API behaviour change → update `internal/api/openapi/openapi.yaml`.
- Roadmap‑relevant change → update [`docs/roadmap.md`](../docs/roadmap.md).
- Driver maturity change → update [`DRIVERS.md`](../DRIVERS.md).
- Operator‑visible change (config, env vars, deployment) → update
  [`docs/configuration.md`](../docs/configuration.md) and / or
  [`docs/deployment.md`](../docs/deployment.md).
- New operational hazard or recovery procedure → update
  [`docs/runbooks.md`](../docs/runbooks.md).

## 10. Things AI agents should refuse to do silently

- Push a "completion" of multiple roadmap follow‑up items in one PR.
- Fabricate UI screenshots or other binary assets.
- Edit a released SQL migration.
- Remove or weaken tenant scoping or RBAC.
- Replace a documented stub with a fake implementation.
- Introduce a new dependency without checking the GitHub advisory database
  first and recording the rationale in the PR.

When in doubt, stop and ask the requester to scope the change.
