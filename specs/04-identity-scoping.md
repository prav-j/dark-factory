# 04 — Identity & Scoping Model

This is the heart of the design. Agents are not principals; they are **delegates of a user**.

## Delegated Identity

1. User authenticates to the platform (OIDC).
2. For each external system (GitHub, Slack, Google Drive…), the user completes OAuth **on behalf of themselves**. Tokens are stored in the Secret Manager encrypted per-user.
3. When an agent runs, the Orchestrator mints a short-lived **Run Token**:

```json
{
  "sub": "run_01J9...",
  "session": "sess-01j9x7",
  "agent": "repo-triage-bot@v7",
  "acting_as": "user-1234",
  "org": "org-99",
  "grants": ["github:repo:issues:write", "web_search:read"],
  "mcp_servers": ["github-official"],
  "exp": "+15m",
  "jti": "..."
}
```

4. Tool Gateway and MCP Proxy validate this token on every call. No token → no egress.

> **Token lifetime rule:** tokens are minted per-run with a 15-min TTL but are **renewable by the harness** while the parent session is alive (bound to `session` id, re-minted via the Orchestrator). Sessions may live up to `maxLifetime` (§16); a token never outlives its run. This supersedes any reading that a 15-min token caps session length.

## Permission Broker & Policy Engine

- Central service evaluating every requested capability: `can(user, action, resource, context?)`.
- Backed by a policy layer (Rego/Cedar-style): org policies + user consents + agent spec scopes. Effective scope = intersection of all three.
- Consent flows: first time an agent wants a new scope, the user gets an explicit approval prompt (recorded in `grants` with consent evidence). Progressive disclosure — least privilege by default.
- Revocation is immediate: revoking a grant invalidates cached tokens and blocks at the gateway within seconds.

## Tenant Isolation Layers

| Layer | Mechanism |
|---|---|
| Network | Sandboxes have no direct internet; egress only via gateways (allowlist proxies) |
| Compute | One sandbox (microVM pod) per **session**; no shared filesystem between sessions |
| Data | Row-level security keyed on org/user; object store prefix-per-tenant + KMS keys per org |
| Secrets | Per-user envelope encryption; secrets injected just-in-time into gateway, never into the sandbox env |
| Prompt/data | Cross-tenant retrieval impossible — vector stores partitioned per tenant |
