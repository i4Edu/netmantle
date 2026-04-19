# Roadmap

This repository lands a single-PR sweep of **Phases 0 through 10** of the
NetMantle plan. Each phase is implemented in MVP form: the data model,
service code, REST endpoints, and at least one unit test exist; advanced
hardening, additional drivers, and a polished UI are explicit follow-up
work.

| Phase | Theme | Status |
| ----- | ----- | ------ |
| 0 | Project foundation | ✅ shipped |
| 1 | MVP backup (inventory, SSH, drivers, git versioning, basic UI) | ✅ shipped |
| 2 | Change management & notifications | ✅ shipped (MVP) |
| 3 | Auditing & search | ✅ shipped (MVP) |
| 4 | Configuration compliance | ✅ shipped (MVP) |
| 5 | Discovery & NMS sync | ✅ shipped (TCP/banner scan + NetBox JSON; SNMP + LibreNMS/Zabbix follow-up) |
| 6 | Push / pull automation | ✅ shipped (preview + grouping; per-driver Apply hook is follow-up) |
| 7 | In-app CLI & distributed pollers | ✅ shipped (web terminal + poller registration; on-the-wire gRPC poller protocol is follow-up) |
| 8 | Runtime state auditing & compliance | ✅ shipped (MVP) |
| 9 | Multi-tenancy & HA | ✅ shipped (tenant CRUD + quotas + leader-elected scheduler + Helm chart) |
| 10 | Hardening, NETCONF/RESTCONF/gNMI, topology, GitOps mirror | ✅ partial (NETCONF helpers, stub drivers for RESTCONF/gNMI, topology builder, GitOps mirror, signed releases + SBOM workflow; full NETCONF backup wiring + topology UI are follow-up) |

## Known follow-up work

- gRPC wire protocol for pollers (currently registration + heartbeat only).
- Per-driver `Apply()` to enable live config push (currently preview-only).
- NETCONF backup driver wiring (helpers exist; integration with the backup
  flow is staged for the next PR).
- Topology UI (the API endpoint exists today).
- Driver SDK + community driver registry.
- Pen-test, threat-model document, and SOC-style runbooks.
