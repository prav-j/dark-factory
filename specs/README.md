# Managed Agents Platform — Spec Index

**Status:** Draft v1 · **Owner:** Platform Team · **Last updated:** 2026-08-21

The original monolithic design is archived at [`archive/managed-agents-system-design.md`](archive/managed-agents-system-design.md). The authoritative breakdown lives in the numbered files below. Cross-references use `§NN` pointing at file numbers.

| # | File | Scope |
|---|---|---|
| 01 | [overview.md](01-overview.md) | Goals, non-goals, core promise |
| 02 | [architecture.md](02-architecture.md) | Three-plane architecture, trust boundaries |
| 03 | [data-model.md](03-data-model.md) | Agent spec, entities, store split (PG vs DDB) |
| 04 | [identity-scoping.md](04-identity-scoping.md) | Delegated identity, run tokens, policy engine, isolation |
| 05 | [execution-flow.md](05-execution-flow.md) | Chat/autonomous runs, agent loop runtime |
| 06 | [tool-gateway.md](06-tool-gateway.md) | Tool registry, call pipeline, egress control |
| 07 | [mcp-proxy.md](07-mcp-proxy.md) | MCP connections, per-user routing, tool filtering |
| 08 | [secrets.md](08-secrets.md) | Envelope encryption, rotation, scrubbing |
| 09 | [scaling-cost.md](09-scaling-cost.md) | Scaling, budgets, rate limits, noisy neighbors |
| 10 | [observability-audit.md](10-observability-audit.md) | Tracing, transcripts, audit log |
| 11 | [threat-model.md](11-threat-model.md) | Threats & mitigations |
| 12 | [api.md](12-api.md) | Consolidated API surface (single source of truth) |
| 13 | [tech-stack.md](13-tech-stack.md) | Proposed stack decisions |
| 14 | [delivery-plan.md](14-delivery-plan.md) | Phases, open questions |
| 15 | [executor-environments.md](15-executor-environments.md) | Custom images, setup scripts, repos, snapshots |
| 16 | [deployment-sessions.md](16-deployment-sessions.md) | K8s operator, session lifecycle, stop hook, DDB session store |
| 17 | [live-readiness-roadmap.md](17-live-readiness-roadmap.md) | Path to full conformance + local live run (kind gate) + cloud staging |
