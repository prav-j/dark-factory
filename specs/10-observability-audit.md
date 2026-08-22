# 10 — Observability & Audit

- **Tracing**: OpenTelemetry spans per run — every LLM call, tool call, MCP call correlated by `run_id`.
- **Transcripts**: full message history persisted to object store; metadata pointers in DynamoDB (`Sessions`/`Runs`, §16.3) and immutable billing/audit lineage in Postgres (`run_records`, §03).
- **Audit log**: append-only record of every authorization decision (who/what/allowed-denied/why) — this is the compliance backbone (SOC2, GDPR).
- **Live monitoring**: run dashboard, error rates, latency percentiles, spend dashboards.
- **Executor metrics**: build time, restore time, warm-hit ratio, snapshot size per agent/org (§15.6).
- **Evaluation hooks**: optional eval suites attached to agent versions; regression gating on publish.
