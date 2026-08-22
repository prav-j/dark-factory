# 09 — Multi-Tenancy, Scaling & Cost

## Scaling

- **Orchestrator**: horizontally scaled, queue-backed (Redis Streams / SQS). Partition by user for ordering guarantees.
- **Session sandboxes**: warm pools of pre-forked microVM pods managed by the K8s operator (§16.1), keyed by environment snapshot cache key (§15.3).
- **Start-latency targets** (supersedes the earlier single "< 500ms" claim):
  - Hot-pool fork: **< 500ms** target
  - Snapshot restore from object store: **2–5s**
  - Cold environment build: minutes (async; run queued with "provisioning" status)
- **Model Gateway**: provider-agnostic router with retries, fallbacks, semantic caching (opt-in), and streaming.
- **Stateless services** scale on CPU/RPS; sandboxes scale on queue depth.

## Cost Controls

- Token + dollar metering per run, aggregated per agent/user/org.
- Hard budget enforcement (reject runs), soft alerts (notify at 80%).
- Rate limits at four levels: user, agent, org, tool.

## Noisy Neighbor Protection

Fair-share scheduling in the orchestrator; per-tenant quotas on sandbox allocation; separate queues for interactive vs background workloads (interactive always prioritized). Enforced in k8s via ResourceQuotas + PriorityClasses (§16.1).
