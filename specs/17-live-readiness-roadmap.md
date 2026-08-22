# 17 — Live Readiness Roadmap

**Status:** Draft v1 · **Owner:** Platform Team · **Last updated:** 2026-08-21

Path from current state (30/30 build issues closed, 7/45 conformance checks passing, everything tested behind interfaces) to **full spec conformance** and a **validated local live run**, before any cloud deployment.

## Guiding constraints

- Every issue is scoped to a single S–M sized PR (roughly ≤400 LOC or one coherent concern).
- **Gate:** no cloud work starts until W3's local live scenario passes end-to-end in kind.
- Conformance checks are flipped only by real assertions — never by reclassifying.

---

## W1 — Flip implemented checks (~20 of 38 pending)

These verify shipped behavior; only assertion authorship is needed. All container/unit-backed via the existing conformance job.

| Issue | Scope | Flips |
|---|---|---|
| W1.1 | Registry immutability + store-split assertions | C03-001, C03-003 |
| W1.2 | Composed token lifecycle: mint → gateway call → revoke → deny → renew-fails | C04-002, C04-003, C04-005 |
| W1.3 | Policy intersection wrapper (org ∩ consent ∩ spec) | C04-004 |
| W1.4 | Harness wrappers: grant filtering, HITL round-trip, git-durable model, webhook idempotency | C05-001..004 |
| W1.5 | Tool gateway + git facade wrappers: pipeline order, no-token denial, egress block, remote rewrite | C06-001..004 |
| W1.6 | MCP proxy wrappers: filtering + namespacing | C07-002, C07-003 |
| W1.7 | Orchestrator/budget wrappers: per-level limits, interactive priority | C09-001..002 |
| W1.8 | API drift review: OpenAPI vs routes vs specs/12; unauthenticated-rejection check | C12-001, C12-002 |

Delegated identity (C04-001) flips in W2 once OIDC→run-token→gateway is one continuous flow.

## W2 — Missing production backends

Interfaces exist; the real implementations don't yet.

| Issue | Scope |
|---|---|
| W2.1 | DDB-backed `SessionChecker` adapter for run-token renewal |
| W2.2 | DDB/object-store `Checkpointer` + `Persister` adapters for harness + stop hook |
| W2.3 | KMS `RootKeyProvider` (AWS KMS; static provider stays for tests) |
| W2.4 | Trivy `Scanner` implementation (CLI invocation against registry digest) |
| W2.5 | BuildKit `DockerBuilder` shell-out (rootless buildctl against ephemeral builder) |
| W2.6 | Secret-scrubbing middleware for transcripts/logs (C08-002) |
| W2.7 | MCP streamable HTTP transport client implementing `Session` |
| W2.8 | SSE streaming from harness through the API layer |
| W2.9 | Continuous delegated-identity flow test (OIDC → user token → run token → gateway) — flips C04-001 |
| W2.10 | OTLP exporter wiring + span instrumentation in registry/orchestrator/gateways (C10-001) |

## W3 — Local live run in kind (**the gate**)

Target: the full live scenario runs locally with no cloud dependencies beyond images on a public registry (or locally loaded).

| Issue | Scope |
|---|---|
| W3.1 | Containerize all services (registry, orchestrator, operator, mockoidc, sandbox-harness base image); Makefile targets to build+load into kind |
| W3.2 | Generate CRD YAML (controller-gen) + install/deploy manifests for operator into kind; bootstrap script `make live-up` |
| W3.3 | Session pod = harness binary: entrypoint wires run token, transcript ref, tool/MCP/git endpoints; orchestrator dispatches runs into pods via API |
| W3.4 | **Live scenario runner**: scripted end-to-end — onboard user, register GitHub connection, publish agent spec (custom Dockerfile + pinned repo), chat trigger, warm-pool fork, clone via facade, open PR via tools, idle → stop-hook push, resume continues work. Asserts manifest + PR existence |
| W3.5 | Node-drain / PDB drill in kind: drain mid-commit session, assert stop hook completes and work survives (flips C16-002/003 evidence quality) |
| W3.6 | Flip infrastructure-dependent checks from live evidence: C04-006, C15-003, C16-001, C16-005 (+ upgrade C16-002/003 if stronger than fake-client versions) |

Exit criteria for W3: `make live-up && make live-scenario` green on a clean machine; conformance ≥ ~35/45.

## W4 — Cloud staging + hardening (gated on W3)

| Issue | Scope |
|---|---|
| W4.1 | Terraform/bootstrap: managed k8s, RDS Postgres, ElastiCache Redis, real DynamoDB, image registry |
| W4.2 | Real OIDC provider integration (replace mockoidc), DNS/TLS |
| W4.3 | Load test orchestrator: queue depth, shard contention, worker scaling |
| W4.4 | Chaos drills: provider outage → fallback, Redis loss behavior, DDB throttling |
| W4.5 | Backup/restore rehearsal (Postgres PITR, object-store versioning) |
| W4.6 | Dashboards + alerting from #29 metrics; spend dashboards from #27 |

Explicitly deferred beyond this roadmap (specs/14 phase 3): marketplace, multi-region, SOC2 evidence collection, enterprise SSO/SCIM.

---

## Sequencing

```
W1 (flip cheap checks) ──► W2.1-W2.3, W2.6 (small adapters) ──►
W3.1-W3.3 (kind bring-up) ──► W3.4 (LIVE SCENARIO GATE) ──►
W2 remainder as needed by scenario gaps ──► W3.5-W3.6 ──► W4
```

Rationale: W1 is pure wins; kind bring-up only needs small adapters (W2.1–W2.3) plus containerization, not every production backend — LocalStack/fakes remain acceptable for the local gate where they already exist.
