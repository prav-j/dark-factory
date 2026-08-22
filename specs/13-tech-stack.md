# 13 — Tech Stack (proposed)

| Component | Choice |
|---|---|
| Control services | Go or TypeScript (matching team), gRPC internal / REST external |
| Sandbox runtime | Kubernetes operator + Firecracker/gVisor microVM pods (§16) |
| Session/run state | DynamoDB — execution-plane operational store (§16.3) |
| Control metadata | Postgres with RLS — source of truth for agents, grants, registry, billing lineage (§03) |
| Queue/Scheduler | Redis Streams → Kafka at scale; cron service on top |
| Cache | Redis (queues/cache only — not sessions) |
| Blobs/transcripts/snapshots | S3-compatible object store |
| Vectors | pgvector initially; dedicated vector DB later |
| Secrets | Vault or cloud KMS + Secret Manager |
| Observability | OpenTelemetry → Grafana stack / Datadog |
| Policy | Cedar or OPA |

> **Store split rule:** anything a running session reads/writes at high frequency lives in DDB/object store; anything that must be relationally queried, billed, or audited long-term lands in Postgres and the append-only audit log.
