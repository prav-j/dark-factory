# 16 — Deployment Model: Kubernetes Operator & Session Lifecycle

## Operator Architecture

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
  phase: Provisioning | Running | Idle | Committing | Terminated
  pod: ...
  warmHit: true
```

Controller responsibilities:

- Watch `AgentSession` CRs → create/update pods from the environment snapshot (§15), attach COW overlay volume.
- **Idle detection**: no inbound events (user messages, tool activity) and no active LLM stream for `idleTimeout` → transition `Running → Idle`.
- **Graceful shutdown sequence** on idle or explicit stop:

```
Running ──idle timeout──► Idle ──► Committing ──► Terminated
                            │           │
                     stop hook fires    work committed to git,
                     (§16.2)            transcript flushed,
                                        overlay discarded
```

- Warm pool management: pre-fork pods per hot cache key as separate low-priority deployments, promoted into sessions on demand. The **operator is the single owner** of all pools (supersedes the generic "warm pool sized by forecast" language in §09).
- Per-org ResourceQuotas + PriorityClasses (interactive > background); PodDisruptionBudgets so node drains don't kill mid-commit sessions.

> Note: there is deliberately **no per-session `Snapshotting` phase**. Snapshots exist only at environment-build time (§15); session-end state is captured via git + manifest, not filesystem snapshots.

## The Stop Hook (commit-before-death contract)

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

## Session Store (DynamoDB)

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
- Postgres remains the source of truth for control-plane entities (agents, grants, registry, completed-run lineage as `run_records`, §03); DDB is the execution plane's operational store.

## Resume Flow

```
POST /v1/runs { session: { resume: "sess-01j9x7" } }     # §12
  1. DDB lookup → fetch manifest + status
  2. Resolve branches at headSha (verify they still exist / PR merged?)
  3. Fork fresh pod from environmentKey snapshot
  4. Clone repos, checkout manifest branches
  5. Hydrate context: transcript summary + last N messages + open PR states
  6. Surface uncommitted-diff recovery if flagged
  7. Continue agent loop
```
