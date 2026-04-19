# ARCHITECTURE

This document is a reviewer-focused summary of current architecture and
limitations. Detailed package-level design remains in
[`docs/architecture.md`](docs/architecture.md) and ADRs under [`docs/adr/`](docs/adr/).

## Current design choices

- **Modular monolith**: one `netmantle` binary with clear `/internal` module
  boundaries to enable future extraction.
- **API-first core**: REST API drives both automation and embedded UI.
- **SQLite-first persistence** with append-only SQL migrations in
  `internal/storage/migrations` (PostgreSQL target remains follow-up).
- **Git-backed configuration history** via `internal/configstore`.
- **Pluggable device access layer** via `internal/drivers` and
  `internal/transport`.

## What is production-ready vs scaffolded

- **Ready in MVP scope**: inventory, backup orchestration, config versioning,
  diff/change events, compliance engine, push-job preview, tenant-aware core,
  signed release artifacts + SBOM generation.
- **Scaffolded / partial**:
  - NETCONF/RESTCONF/gNMI drivers are registered but not hardened end-to-end.
  - Pollers support registration/heartbeat; full gRPC wire protocol is pending.
  - Topology support is API-first (LLDP/CDP graph builder) without full UI.
  - HA behavior exists (leader election) but requires deeper automated failover
    and scale validation.

## API and storage stability policy (pre-v1)

- API routes and payloads can evolve until first stable tag.
- Migration files are append-only; existing released migrations must not be
  rewritten.
- Any schema evolution should include compatibility notes and test coverage in
  the same PR.
