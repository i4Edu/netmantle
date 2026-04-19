# AGENTS.md

This file is for AI coding agents (and any tool that conventionally reads an
`AGENTS.md` at the repository root). It mirrors
[`.github/copilot-instructions.md`](.github/copilot-instructions.md) — please
read that document. It contains:

- the repository layout and mental model,
- the canonical `make` build/lint/test commands,
- the Go coding conventions used throughout `internal/`,
- the schema‑migration and OpenAPI rules,
- security guardrails (tenancy, RBAC, secret handling),
- and — most importantly — the **shipped vs scaffolded** rule that prevents
  agents from silently turning documented stubs into fake implementations.

Keep `AGENTS.md` and `.github/copilot-instructions.md` in sync when either is
edited.
