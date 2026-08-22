# System Design: Managed Agents Platform

**Status:** Draft v1
**Owner:** Platform Team
**Last updated:** 2026-08-21

---

## 1. Overview

A multi-tenant platform that lets any user define, deploy, and operate AI agents of their choice — each configured with a model, prompt/persona, tools, and MCP (Model Context Protocol) servers — with all access strictly scoped to the owning user's identity and permissions.

Core promise: **"Bring your own agent; we run it safely on your behalf."**

### 1.1 Goals

- Users define agents declaratively (spec file or UI) — model, system prompt, tools, MCP servers.
- Tools and MCP servers execute **as the user**, scoped to the user's credentials and grants — never the platform's.
- Strong tenant isolation: one user's agent can never touch another's data, secrets, or sessions.
- Managed lifecycle: versioning, deployment, monitoring, rate limiting, audit logs.
- Support both interactive (chat) and autonomous/scheduled (background) execution.

### 1.2 Non-Goals (v1)

- Training or fine-tuning models.
- Marketplace for sharing agents across tenants (future).
- Self-hosted / BYO-infra execution (future enterprise tier).

---

## 2. High-Level Architecture

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
                        │  Orchestrator ──► Agent Runtime Pool        │
                        │    (queues,     (sandboxed containers,      │
                        │     schedulers)  one session = one sandbox) │
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
                        │  Postgres (metadata) · Object Store         │
                        │  Redis (sessions/queue) · Vector DB         │
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

---

## 3. Core Concepts & Data Model

### 3.1 Agent Spec (declarative)

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

### 3.2 Entities (Postgres)

| Entity | Key fields | Notes |
|---|---|---|
| `users` | id, org_id, auth identities | Org → User hierarchy |
| `agents` | id, owner_user_id, org_id, current_version | Immutable versions |
| `agent_versions` | spec JSON (canonicalized), hash, status | draft/published/deprecated |
| `tool_registry` | ref, kind (builtin/mcp), schema, required scopes | Global catalog + private entries |
| `mcp_connections` | user_id, server_ref, credential_ref, granted_scopes | Per-user MCP authorization |
| `grants` | user_id, resource, scope, expiry, consent_record | The permission ledger |
| `runs` | id, agent_version_id, trigger, status, token/cost usage | Full lineage |
| `messages` | run_id, role, content refs | Content blobs in object store |
| `secrets` | id, user_id, ciphertext, kek_version | Envelope-encrypted |
| `audit_events` | actor, action, resource, decision, reason | Append-only |

Key invariant: **every capability an agent uses resolves through a `grant` tied to the human user who owns the run.**

---

## 4. Identity & Scoping Model

This is the heart of the design. Agents are not principals; they are **delegates of a user**.

### 4.1 Delegated Identity

1. User authenticates to the platform (OIDC).
2. For each external system (GitHub, Slack, Google Drive…), the user completes OAuth **on behalf of themselves**. Tokens are stored in the Secret Manager encrypted per-user.
3. When an agent runs, the Orchestrator mints a short-lived **Run Token**:

```json
{
  "sub": "run_01J9...",
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

### 4.2 Permission Broker & Policy Engine

- Central service evaluating every requested capability: `can(user, action, resource, context?)`.
- Backed by a policy layer (Rego/Cedar-style): org policies + user consents + agent spec scopes. Effective scope = intersection of all three.
- Consent flows: first time an agent wants a new scope, the user gets an explicit approval prompt (recorded in `grants` with consent evidence). Progressive disclosure — least privilege by default.
- Revocation is immediate: revoking a grant invalidates cached tokens and blocks at the gateway within seconds.

### 4.3 Tenant Isolation Layers

| Layer | Mechanism |
|---|---|
| Network | Sandboxes have no direct internet; egress only via gateways (allowlist proxies) |
| Compute | One container/microVM per run (Firecracker/gVisor); no shared filesystem between runs |
| Data | Row-level security keyed on org/user; object store prefix-per-tenant + KMS keys per org |
| Secrets | Per-user envelope encryption; secrets injected just-in-time into gateway, never into the sandbox env |
| Prompt/data | Cross-tenant retrieval impossible — vector stores partitioned per tenant |

---

## 5. Execution Flow

### 5.1 Interactive Chat Run

```
User → API GW → Session Service → Orchestrator
  1. Resolve agent version, load spec
  2. Permission Broker: compile effective grant set for this user+agent
  3. Allocate sandbox from warm pool; inject Run Token (not raw secrets)
  4. Agent loop (in Runtime):
       LLM call ⇄ Model Gateway (rate limits, cost metering, provider failover)
       tool call → Tool Gateway → checks token+policy → executes with user creds
       MCP call  → MCP Proxy → routes to user's registered MCP connection
  5. Stream events (SSE) back to user
  6. Telemetry, cost accounting, transcript persisted
  7. Sandbox destroyed; ephemeral state discarded
```

### 5.2 Autonomous Runs (scheduled/webhook)

Same pipeline, triggered by Scheduler (cron evaluation service) or Webhook Ingress. Additional guardrails:

- Budget checks before start (`monthlyBudgetUsd`).
- Idempotency keys for webhook triggers.
- Result delivery via user-configured channels (email, Slack, callback URL) — again through permission-checked gateways.
- Kill switch: pause/resume per agent, per user, global circuit breaker.

### 5.3 Agent Loop Runtime

Provider-agnostic loop supporting major agentic patterns:

- ReAct/tool-calling loop with configurable `maxStepsPerRun`.
- Context assembly: system prompt + memory + tool schemas (only tools in the effective grant set are exposed to the model).
- Streaming, retries with backoff, checkpointing after each step so long runs can resume after infra failures.
- Human-in-the-loop: tools marked `requiresApproval: true` pause the run and emit an approval request to the user; run resumes on approve/deny.

---

## 6. Tool Gateway

Single choke point for all non-MCP tool calls.

- **Tool Registry**: built-in tools (web search, code interpreter, HTTP request) + user-defined tools (OpenAPI spec → auto-generated tool schema).
- On each call: authenticate Run Token → evaluate policy → apply rate limits → inject user-scoped credentials → execute → redact/normalize response.
- Response filtering: PII/DLP scanning hooks, size caps, content-type allowlists.
- Egress control: HTTP tool runs through an outbound proxy with domain allowlists configurable per org.

## 7. MCP Proxy

MCP is first-class; users may register any MCP server.

- **Connection management**: users register MCP servers (streamable HTTP preferred; stdio servers run in platform-managed sidecar sandboxes). OAuth-based MCP auth stored per-user.
- **Per-user routing**: proxy maps `(run_token.user, server_ref)` → that user's connection and credentials. Two users on the same MCP server never share sessions or caches.
- **Tool filtering**: `allowedTools` in the agent spec intersects with the user's granted scopes — the model only ever sees the resulting tool list.
- **Namespacing**: MCP tools exposed to the agent as `mcp__<server>__<tool>` to avoid collisions.
- Protocol handling: JSON-RPC multiplexing, notification fan-out, timeout/retry policy, schema validation of responses before they enter model context.

---

## 8. Secrets Management

- KMS root key per environment; DEKs per user/org (envelope encryption).
- Secrets referenced by ID in agent specs; resolved only inside gateways at call time.
- Short-lived credential exchange where possible (token exchange to downstream providers rather than long-lived PATs).
- Rotation jobs, access logging on every secret read, no secret ever appears in transcripts/logs (scrubbing middleware).

---

## 9. Multi-Tenancy, Scaling & Cost

### 9.1 Scaling

- **Orchestrator**: horizontally scaled, queue-backed (Redis Streams / SQS). Partition by user for ordering guarantees.
- **Sandboxes**: warm pool of microVMs sized by forecast; cold-start target < 500ms; per-org concurrency caps.
- **Model Gateway**: provider-agnostic router with retries, fallbacks, semantic caching (opt-in), and streaming.
- **Stateless services** scale on CPU/RPS; sandboxes scale on queue depth.

### 9.2 Cost Controls

- Token + dollar metering per run, aggregated per agent/user/org.
- Hard budget enforcement (reject runs), soft alerts (notify at 80%).
- Rate limits at four levels: user, agent, org, tool.

### 9.3 Noisy Neighbor Protection

Fair-share scheduling in the orchestrator; per-tenant quotas on sandbox allocation; separate queues for interactive vs background workloads (interactive always prioritized).

---

## 10. Observability & Audit

- **Tracing**: OpenTelemetry spans per run — every LLM call, tool call, MCP call correlated by `run_id`.
- **Transcripts**: full message history persisted (object store + metadata in PG), replayable in UI.
- **Audit log**: append-only record of every authorization decision (who/what/allowed-denied/why) — this is the compliance backbone (SOC2, GDPR).
- **Live monitoring**: run dashboard, error rates, latency percentiles, spend dashboards.
- **Evaluation hooks**: optional eval suites attached to agent versions; regression gating on publish.

---

## 11. Security Threat Model (highlights)

| Threat | Mitigation |
|---|---|
| Prompt injection exfiltrates data | Egress allowlists, DLP filters on tool responses, mark untrusted content in context, approval gates for sensitive tools |
| Agent escalates privileges | Static grant set minted at run start; no self-service grant escalation without user consent flow |
| Cross-tenant leakage | RLS + per-tenant encryption keys + network isolation; chaos tests assert isolation |
| Malicious MCP server | Registry vetting/scanning, tool-response sanitization, per-server sandboxing, user-visible warnings for unverified servers |
| Stolen run token | 15-min TTL, bound to run + sandbox instance, jti revocation list |
| Runaway agent (cost/actions) | Step/token/budget caps, kill switch, anomaly detection on action velocity |

---

## 12. API Surface (v1 sketch)

```
POST   /v1/agents                      # create draft
PUT    /v1/agents/{id}/versions        # new version
POST   /v1/agents/{id}:publish
POST   /v1/runs                        # {agent, input, stream: true}
GET    /v1/runs/{id}                   # status, usage
POST   /v1/runs/{id}:cancel
POST   /v1/runs/{id}/approvals         # HITL decisions
GET    /v1/tools                       # catalog
POST   /v1/mcp/connections             # register MCP server
POST   /v1/grants                      # consent records
GET    /v1/audit?resource=...
```

CLI + Web UI are thin clients over the same API. Agent specs are portable files (GitOps-friendly).

---

## 13. Tech Stack (proposed)

| Component | Choice |
|---|---|
| Control services | Go or TypeScript (matching team), gRPC internal / REST external |
| Sandbox | Kubernetes operator + Firecracker/gVisor microVM pods (§16) |
| Session store | DynamoDB (execution plane) — Postgres stays control-plane source of truth |
| Queue/Scheduler | Redis Streams → Kafka at scale; cron service on top |
| Primary store | Postgres (with RLS) |
| Blobs/transcripts | S3-compatible object store |
| Cache/session | Redis |
| Vectors | pgvector initially; dedicated vector DB later |
| Secrets | Vault or cloud KMS + Secret Manager |
| Observability | OpenTelemetry → Grafana stack / Datadog |
| Policy | Cedar or OPA |

---

## 14. Delivery Plan

**Phase 1 — MVP (6–8 wks):** chat-triggered agents, built-in tools, single MCP server support, per-user OAuth, basic budgets, transcripts. Single region.

**Phase 2:** scheduled/webhook triggers, HITL approvals, user-defined tools (OpenAPI import), policy engine, full audit log, multi-region read.

**Phase 3:** agent marketplace (private sharing), eval-gated publishing, advanced DLP, SOC2 audit readiness, enterprise SSO/SCIM.

### Key open questions

1. Stdio MCP servers: run them platform-side (managed sidecars) vs require remote servers only?
2. Memory semantics: shared per-agent across runs vs per-conversation only?
3. Group/role-based agents (service accounts acting beyond a single user) — defer or design grants for teams now?

---

---

## 15. Executor Deep Dive: Custom Environments, Repos & Snapshots

This section details how executors support user-defined environments (custom Docker images, setup scripts), repository cloning into sessions, and **filesystem snapshotting for warm starts**.

### 15.1 Environment Model

An agent spec gains an `environment` block:

```yaml
spec:
  environment:
    image:
      type: docker-ref | dockerfile        # prebuilt image OR buildable
      ref: docker.io/acme/dev-python:3.12  # or inline Dockerfile below
      dockerfile: |
        FROM ubuntu:24.04
        RUN apt-get update && apt-get install -y python3 git curl
        USER dev
    setup:                                  # runs ONCE at env-build time
      - run: ./scripts/bootstrap.sh         # from attached repo or inline
      - run: pip install -e ".[dev]"
    resources:
      cpu: 4
      memory: 8Gi
      disk: 20Gi
    repos:                                  # cloned at env-build or session-start
      - url: https://github.com/acme/api-server
        ref: main                           # branch, tag, or pinned SHA
        path: /workspace/api-server
        auth: github-oauth                  # resolved per-user at clone time
    network: allowlist                      # domains user declares for builds
```

Three distinct phases, each cached independently:

```
┌─────────────┐   ┌──────────────────┐   ┌─────────────────────┐
│ Image Build │ → │ Environment Build │ → │ Session Materialize │
│ (or pull)   │   │ setup + clone     │   │ fork from snapshot  │
│  minutes    │   │ + SNAPSHOT        │   │  < 1s (warm)        │
│  cached     │   │  minutes, cached  │   │                     │
└─────────────┘   └──────────────────┘   └─────────────────────┘
```

### 15.2 Image Build Pipeline

- **Prebuilt refs**: pulled through a pull-through cache proxy; scanned (Trivy/Grype) for critical CVEs; blocked if org policy disallows.
- **Dockerfiles**: built by a dedicated, isolated Build Service (BuildKit rootless, one build per ephemeral builder). Builds happen **outside** the execution plane — never inside a running agent sandbox.
- Images stored in a per-tenant namespace of the platform registry, content-addressed by `(dockerfile hash + build args + base digest)` → immutable digest. Identical rebuilds are no-ops.
- Size caps, build timeout, network restricted to declared allowlist during build.

### 15.3 Environment Build & Snapshots

The **Environment Builder** is the core of warm-up:

1. Boot a throwaway microVM from the built/pulled image.
2. Run `setup` steps (scripts execute here, not per-session).
3. Clone repos using the **owning user's** credentials injected just-in-time via a git credential-helper that calls back to the Secret Manager (credentials never written to disk in the snapshot).
4. Quiesce: kill processes, drop tmpfs, clear logs/shell history, scrub anything secret-shaped (scan pass).
5. Take a **snapshot**, store in object storage, record metadata.

**Snapshot technology options:**

| Approach | What's captured | Restore speed | Notes |
|---|---|---|---|
| Filesystem snapshot (btrfs/ZFS send-receive, or overlay upper-dir tarball) | Disk state only | ~seconds (boot fresh VM + mount) | Simplest, most portable; default v1 |
| Firecracker VM state snapshot (mem + device state) | RAM + disk | ~100–300ms | Fastest; requires same host CPU/features; use for hot pool |
| Hybrid | FS snapshot always; VM-state snapshot opportunistically | Best of both | Recommended target |

Snapshots are content-addressed by a **cache key**:

```
key = H( image_digest
       + hash(setup_steps)
       + repo_pins[url → resolved SHA]
       + builder_version
       + base_image_patch_level )
```

Same key → reuse snapshot, skip the minutes-long build. Any input change → new key → rebuild async while the current snapshot keeps serving (build-ahead).

### 15.4 Session Materialize (cold vs warm)

On run start, the Orchestrator requests an executor slot:

- **Warm hit**: a pooled microVM was pre-forked from the snapshot (hot pool keeps N ready per popular cache key, N driven by forecast + queue depth). Session attaches a fresh writable overlay on top of the snapshot — copy-on-write, so sessions never mutate the shared snapshot. Start latency < 1s.
- **Warm miss**: restore from object-stored FS snapshot onto local NVMe (~2–5s), then boot.
- **Cold**: full environment build (minutes); run is queued and the user sees "environment provisioning" status; subsequent runs are warm.

Per-session writes live only in the overlay. On session end: overlay discarded (ephemeral mode) or committed as a **session checkpoint** (resumable sessions / debugging), size-capped.

### 15.5 Repo Handling During Sessions

- Clones land under `/workspace/<repo>` owned by the sandbox user.
- Git operations *inside* the session (push, PR creation) go through the Tool Gateway's git facade — authenticated as the user, policy-checked, audited. The remote URL is rewritten to point at the facade, so raw credentials are never present in the sandbox.
- Large-repo ergonomics: sparse checkout / partial clone flags configurable; LFS passthrough via gateway.

### 15.6 Lifecycle & Hygiene

- **Invalidation**: snapshots expire (default 7d TTL); base-image CVE patches force rebuild; users can force-refresh (`POST /v1/environments/{id}:rebuild`).
- **Storage economics**: dedupe by chunk-level content addressing (snapshots share blocks across versions); LRU eviction of cold snapshots; per-org snapshot storage quota.
- **Security**: snapshots encrypted per-org KMS key; access scoped to owner user; a snapshot from user A can never be mounted by user B even within the same org, unless explicitly shared via a grant. Secret-scrub scan gates every snapshot publish.
- **Observability**: per-phase metrics (build time, restore time, warm-hit ratio, snapshot size) surfaced per agent/org.

### 15.7 API Additions

```
POST /v1/environments                       # define/rebuild environment
GET  /v1/environments/{id}                  # status: building | ready(snapshot_key)
POST /v1/environments/{id}:rebuild
GET  /v1/snapshots?key=...                  # inspect cache entries
POST /v1/runs  { ..., "session": { "resume": "sess_01J..." } }   # resume checkpointed session
```

---

## 16. Deployment Model: Kubernetes Operator & Session Lifecycle

### 16.1 Operator Architecture

The execution plane runs as a **Kubernetes operator** (custom controller + CRDs). The Orchestrator doesn't manage pods directly — it writes intent; the controller reconciles.

```yaml
apiVersion: agents.platform/v1
kind: AgentSession
metadata:
  name: sess-01j9x7
  namespace: tenant-org99            # namespace-per-org hard isolation
spec:
  agentRef: repo-triage-bot@v7
  userId: user-1234
  environmentKey: sha256:a1b2...     # snapshot cache key (§15)
  resources: { cpu: "4", memory: 8Gi }
  idleTimeout: 10m
  maxLifetime: 4h
status:
  phase: Running | Idle | Committing | Snapshotting | Terminated
  pod: ...
  warmHit: true
```

Controller responsibilities:

- Watch `AgentSession` CRs → create/update pods from the environment snapshot (§15.4), attach COW overlay volume.
- **Idle detection**: no inbound events (user messages, tool activity) and no active LLM stream for `idleTimeout` → transition `Running → Idle`.
- **Graceful shutdown sequence** on idle or explicit stop:

```
Running ──idle timeout──► Idle ──► Committing ──► Terminated
                            │           │
                     stop hook fires    work committed to git,
                     (§16.2)            transcript flushed,
                                        overlay discarded
```

- Warm pool management: pre-fork pods per hot cache key as separate low-priority deployments, promoted into sessions on demand.
- Per-org ResourceQuotas + PriorityClasses (interactive > background); PodDisruptionBudgets so node drains don't kill mid-commit sessions.

### 16.2 The Stop Hook (commit-before-death contract)

The agent harness registers a **stop hook** that fires whenever a session is about to end (idle shutdown, max lifetime, user stop, preemption warning from the API server):

1. Hook injects a final system instruction into the loop: *"Session ending. Ensure all work is persisted: commit and push all changes to your working branch, open/update PRs with clear descriptions, and summarize state."*
2. Harness waits (bounded grace period, e.g. 120s) for the model to finish its commit/push round-trips through the git facade.
3. Harness emits a **Session Manifest** before exit:

```json
{
  "sessionId": "sess-01j9x7",
  "agentVersion": "repo-triage-bot@v7",
  "transcriptRef": "s3://transcripts/org99/sess-01j9x7.jsonl",
  "gitState": [
    { "repo": "acme/api-server", "branch": "agent/sess-01j9x7/fix-auth",
      "headSha": "e3f1...", "prs": ["#482"], "uncommitted": false }
  ],
  "endedReason": "idle-timeout"
}
```

This makes **git the durable workspace**: the sandbox filesystem is fully disposable. Resume needs only the transcript + branch/PR refs — a fresh pod forks from the base snapshot, clones the manifest's branches at `headSha`, replays the transcript tail into context, and continues.

Edge cases handled by the hook:
- Model fails to commit in time → manifest records `uncommitted: true` + diff blob uploaded to object store; next resume surfaces this to the model first.
- Preemption (node drain): 60s eviction grace via PDB + hook priority; worst case the diff-blob path above preserves work.

### 16.3 Session Store (DynamoDB)

Session/runtime metadata goes in DynamoDB (high write throughput, per-session access patterns, TTL for cleanup):

| Table | PK | SK | Attributes |
|---|---|---|---|
| `Sessions` | `orgId` | `sessionId` | userId, agentVersion, status, manifest, environmentKey, createdAt, expiresAt (TTL) |
| `SessionsByUser` | `userId` | `sessionId` (GSI) | list/resume lookups |
| `Runs` | `sessionId` | `runId` | trigger, usage, cost, status |

Design notes:
- Transcript *content* stays in object store; DDB holds pointers + small manifests only (< 400KB item limit respected).
- Single-table-ish access patterns: "all sessions for org", "resumable sessions for user", "active sessions per agent" via GSIs.
- TTL attribute auto-expires terminated sessions after retention window; audit-relevant fields mirrored to the append-only audit log first.
- Postgres remains the source of truth for control-plane entities (agents, grants, registry); DDB is the execution plane's operational store. This split keeps the control plane relational/queryable and the exec plane scale-friendly.

### 16.4 Resume Flow

```
POST /v1/runs { session: { resume: "sess-01j9x7" } }
  1. DDB lookup → fetch manifest + status
  2. Resolve branches at headSha (verify they still exist / PR merged?)
  3. Fork fresh pod from environmentKey snapshot
  4. Clone repos, checkout manifest branches
  5. Hydrate context: transcript summary + last N messages + open PR states
  6. Surface uncommitted-diff recovery if flagged
  7. Continue agent loop
```

---

*Appendix (to add): detailed sequence diagrams, spec JSON Schema, policy language examples, capacity model.*
