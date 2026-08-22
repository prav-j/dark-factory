# 03 — Core Concepts & Data Model

## Agent Spec (declarative)

```yaml
apiVersion: agents/v1
kind: Agent
metadata:
  name: repo-triage-bot
  owner: user-1234
spec:
  model:
    provider: anthropic
    name: claude-sonnet-4-5
    params: { temperature: 0.2 }
  prompt:
    type: inline | template-ref
    value: "You triage incoming GitHub issues..."
  tools:
    - ref: builtin/web_search
      scopes: [read]
    - ref: github/create_issue
      scopes: [repo:issues:write]
  mcpServers:
    - ref: registry/github-official
      version: "1.4"
      auth: oauth-user          # delegated user identity
      allowedTools: [issues.*, repos.read]
  environment:                  # see §15 for full schema
    image: { ... }
    setup: [ ... ]
    repos: [ ... ]
  memory:
    type: conversation | vector-store
    retention: 30d
  triggers:
    - type: chat                 # interactive
    - type: schedule             # cron: "0 * * * *"
    - type: webhook
  limits:
    maxStepsPerRun: 25
    maxTokensPerRun: 200_000
    monthlyBudgetUsd: 50
```

## Entities & Store Split

Two stores with a strict division of labor (§13, §16.3):

**Postgres — control-plane source of truth (relational, queryable, audited):**

| Entity | Key fields | Notes |
|---|---|---|
| `users` | id, org_id, auth identities | Org → User hierarchy |
| `agents` | id, owner_user_id, org_id, current_version | Immutable versions |
| `agent_versions` | spec JSON (canonicalized), hash, status | draft/published/deprecated |
| `tool_registry` | ref, kind (builtin/mcp), schema, required scopes | Global catalog + private entries |
| `mcp_connections` | user_id, server_ref, credential_ref, granted_scopes | Per-user MCP authorization |
| `grants` | user_id, resource, scope, expiry, consent_record | The permission ledger |
| `run_records` | run_id, agent_version_id, trigger, final status, token/cost usage | Immutable billing/audit lineage written at run completion |
| `messages_index` | run_id, role, content refs | Pointers; content blobs in object store |
| `secrets` | id, user_id, ciphertext, kek_version | Envelope-encrypted |
| `audit_events` | actor, action, resource, decision, reason | Append-only |

**DynamoDB — execution-plane operational state (high write throughput, TTL):**

| Table | PK | SK | Contents |
|---|---|---|---|
| `Sessions` | orgId | sessionId | userId, agentVersion, live status, manifest, environmentKey, expiresAt |
| `Runs` | sessionId | runId | trigger, in-flight status, usage counters |

> **Rule:** the old Postgres `runs`/`messages` entities are split — live run/session state lives in DDB; completed-run lineage and message pointers land in Postgres (`run_records`, `messages_index`). No entity is authoritative in both.

Key invariant: **every capability an agent uses resolves through a `grant` tied to the human user who owns the run.**
