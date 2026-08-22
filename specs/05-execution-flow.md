# 05 — Execution Flow

## Interactive Chat Run

```
User → API GW → Session Service → Orchestrator
  1. Resolve agent version, load spec
  2. Permission Broker: compile effective grant set for this user+agent
  3. Allocate session sandbox from warm pool; inject Run Token (not raw secrets)
  4. Agent loop (in Runtime):
       LLM call ⇄ Model Gateway (rate limits, cost metering, provider failover)
       tool call → Tool Gateway → checks token+policy → executes with user creds
       MCP call  → MCP Proxy → routes to user's registered MCP connection
  5. Stream events (SSE) back to user
  6. Telemetry, cost accounting, transcript persisted
  7. On session end: stop hook commits work to git (§16.2), then overlay discarded
```

> **Persistence model:** the sandbox filesystem is always disposable. Durable state = git branches/PRs + transcript + session manifest (§16). There is no per-session filesystem checkpoint in v1; resumability comes exclusively from §16.4's resume flow.

## Autonomous Runs (scheduled/webhook)

Same pipeline, triggered by Scheduler (cron evaluation service) or Webhook Ingress. Additional guardrails:

- Budget checks before start (`monthlyBudgetUsd`).
- Idempotency keys for webhook triggers.
- Result delivery via user-configured channels (email, Slack, callback URL) — again through permission-checked gateways.
- Kill switch: pause/resume per agent, per user, global circuit breaker.

## Agent Loop Runtime

Provider-agnostic loop supporting major agentic patterns:

- ReAct/tool-calling loop with configurable `maxStepsPerRun`.
- Context assembly: system prompt + memory + tool schemas (only tools in the effective grant set are exposed to the model).
- Streaming, retries with backoff, checkpointing after each step so long runs can resume after infra failures (loop-level checkpoints in DDB/object store — distinct from filesystem state).
- Human-in-the-loop: tools marked `requiresApproval: true` pause the run and emit an approval request to the user; run resumes on approve/deny.
