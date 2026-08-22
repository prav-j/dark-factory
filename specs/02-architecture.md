# 02 — High-Level Architecture

```
                        ┌─────────────────────────────────────────────┐
                        │                Control Plane                │
                        │                                             │
  User ──► API Gateway ─┼─► Agent Registry Service                    │
  (UI/CLI)  (authn/z)   │   (CRUD specs, versions, publish)           │
                        │                                             │
                        │─► Permission Broker                         │
                        │   (user consent, scopes, policy engine)     │
                        │                                             │
                        │─► Secret Manager                            │
                        │   (per-user creds, envelope encryption)     │
                        └──────────────┬──────────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────────┐
                        │              Execution Plane                │
                        │                                             │
                        │  Orchestrator ──► K8s Operator ──► Session   │
                        │    (queues,        (§16)          Pods       │
                        │     schedulers)   one SESSION = one sandbox  │
                        │                          │                  │
                        │              ┌───────────┼───────────┐      │
                        │              ▼           ▼           ▼      │
                        │        Tool Gateway   MCP Proxy   Model GW  │
                        │        (scoped       (per-user      (LLM    │
                        │         tool calls)   MCP routing)  router) │
                        └─────────────────────────────────────────────┘
                                       │
                        ┌──────────────▼──────────────────────────────┐
                        │            Data Plane                       │
                        │  Postgres (control metadata) · DynamoDB     │
                        │  (session/run state) · Object Store         │
                        │  Redis (queues/cache only) · pgvector       │
                        │  Audit Log sink (append-only)               │
                        └─────────────────────────────────────────────┘
```

Three planes, deliberately separated:

| Plane | Responsibility | Trust level |
|---|---|---|
| Control | CRUD, authz, secrets, billing | Trusted |
| Execution | Run agent loops, call tools/MCP | Semi-trusted (sandboxed) |
| Data | Persistence, logs | Trusted |

Agent runtime code is treated as **untrusted**: it only ever talks to the outside world through the Tool Gateway and MCP Proxy, which enforce scoping.

> **Consistency note:** sandbox granularity is standardized as *one session = one sandbox* (§16); a session contains one or more runs. Earlier "per-run sandbox" phrasing is superseded.
