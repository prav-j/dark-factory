# 14 — Delivery Plan

**Phase 1 — MVP (6–8 wks):** chat-triggered agents, built-in tools, single MCP server support, per-user OAuth, basic budgets, transcripts, single region. FS-snapshot warm starts only.

**Phase 2:** scheduled/webhook triggers, HITL approvals, user-defined tools (OpenAPI import), policy engine, full audit log, multi-region read, session resume via stop-hook manifests (§16), hot pools + VM-state snapshots.

**Phase 3:** agent marketplace (private sharing), eval-gated publishing, advanced DLP, SOC2 audit readiness, enterprise SSO/SCIM.

## Key open questions

1. Stdio MCP servers: run them platform-side (managed sidecars) vs require remote servers only?
2. Memory semantics: shared per-agent across runs vs per-conversation only?
3. Group/role-based agents (service accounts acting beyond a single user) — defer or design grants for teams now?
4. Branch-tracking repos (e.g., `ref: main`) invalidate snapshot cache keys on every upstream commit — accept frequent rebuilds, or clone floating refs at session start instead of env-build time? (§15.1)
