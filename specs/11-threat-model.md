# 11 — Security Threat Model (highlights)

| Threat | Mitigation |
|---|---|
| Prompt injection exfiltrates data | Egress allowlists, DLP filters on tool responses, mark untrusted content in context, approval gates for sensitive tools |
| Agent escalates privileges | Static grant set minted at run start; no self-service grant escalation without user consent flow |
| Cross-tenant leakage | RLS + per-tenant encryption keys + network isolation; chaos tests assert isolation |
| Malicious MCP server | Registry vetting/scanning, tool-response sanitization, per-server sandboxing, user-visible warnings for unverified servers |
| Stolen run token | 15-min TTL, renewable only within a live session (§04), bound to session + sandbox instance, jti revocation list |
| Runaway agent (cost/actions) | Step/token/budget caps, kill switch, anomaly detection on action velocity |
| Secrets baked into snapshots | Just-in-time credential injection + secret-scrub scan gates every snapshot publish (§08, §15.3) |
| Uncommitted work lost on shutdown | Stop-hook commit contract + diff-blob fallback (§16.2) |
