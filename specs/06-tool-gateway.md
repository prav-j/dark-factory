# 06 — Tool Gateway

Single choke point for all non-MCP tool calls.

- **Tool Registry**: built-in tools (web search, code interpreter, HTTP request) + user-defined tools (OpenAPI spec → auto-generated tool schema).
- On each call: authenticate Run Token → evaluate policy → apply rate limits → inject user-scoped credentials → execute → redact/normalize response.
- Response filtering: PII/DLP scanning hooks, size caps, content-type allowlists.
- Egress control: HTTP tool runs through an outbound proxy with domain allowlists configurable per org.
- **Git facade** (§15.5): in-session git push/PR operations are proxied here — authenticated as the user, policy-checked, audited; remote URLs inside sandboxes are rewritten to point at the facade so raw credentials never enter the sandbox.
