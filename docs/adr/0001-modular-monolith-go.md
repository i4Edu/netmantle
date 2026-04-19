# ADR 0001: Modular monolith in Go with SQLite-first storage

- **Status:** accepted
- **Date:** 2026-04-18

## Context

NetMantle aims to be a self-hosted Unimus alternative. We need a deployment
story that is "go from nothing to backing-up 1000 devices in 20 minutes",
while keeping a path open to distributed pollers, multi-tenancy, and HA.

We considered:

1. Microservices from day one (REST/gRPC between services).
2. JVM monolith (Spring Boot) — closer to Unimus's stack.
3. Go modular monolith with clean package boundaries.

## Decision

Pick **3**: a Go modular monolith.

- Single static binary, no JVM, no runtime dependencies → matches the rapid
  on-prem deployment goal.
- Goroutines map naturally onto fan-out device polling.
- Package boundaries inside `/internal` (api, drivers, transport, backup,
  configstore, …) are designed so a future `cmd/poller` can embed driver +
  transport and talk to the core over gRPC without touching API code.

Storage starts on **SQLite** for trivial PoCs, with the schema written so it
can be migrated to PostgreSQL (the production target) without changes to
business logic. The DB layer is `database/sql` + plain SQL migrations to keep
that portability honest.

Configuration history is stored as **one git repository per device** under a
configurable root, using `go-git`. This gives us free diffing, blame, and a
GitOps-mirror story later (Phase 10) without inventing a custom format.

## Consequences

- ✅ Trivial deploy, fast iteration, easy testing (no broker required).
- ✅ Clear extraction seams for later distributed/HA work.
- ⚠️ Need discipline to keep package boundaries clean — enforced via
  `internal/` visibility and review.
- ⚠️ SQLite is fine for dev/PoC but production tenants will want Postgres;
  the Postgres driver lands in a follow-up phase.
