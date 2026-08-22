# 15 — Executor Deep Dive: Custom Environments, Repos & Snapshots

How executors support user-defined environments (custom Docker images, setup scripts), repository cloning into sessions, and **filesystem snapshotting for warm starts**.

## Environment Model

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
    repos:
      - url: https://github.com/acme/api-server
        ref: main                           # branch, tag, or pinned SHA
        clonePolicy: env-build | session-start   # see "Repo clone timing" below
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

**Repo clone timing** (resolves an earlier ambiguity):
- `env-build` (default for pinned SHAs/tags): repo is cloned during environment build and baked into the snapshot. Cache key includes the resolved SHA.
- `session-start` (recommended for floating branches like `main`): snapshot contains only the image+setup; repos are cloned fresh per session through the git facade. Keeps cache keys stable; costs a few seconds of clone time per session.

## Image Build Pipeline

- **Prebuilt refs**: pulled through a pull-through cache proxy; scanned (Trivy/Grype) for critical CVEs; blocked if org policy disallows.
- **Dockerfiles**: built by a dedicated, isolated Build Service (BuildKit rootless, one build per ephemeral builder). Builds happen **outside** the execution plane — never inside a running agent sandbox.
- Images stored in a per-tenant namespace of the platform registry, content-addressed by `(dockerfile hash + build args + base digest)` → immutable digest. Identical rebuilds are no-ops.
- Size caps, build timeout, network restricted to declared allowlist during build.

## Environment Build & Snapshots

The **Environment Builder** is the core of warm-up:

1. Boot a throwaway microVM from the built/pulled image.
2. Run `setup` steps (scripts execute here, not per-session).
3. Clone repos (`clonePolicy: env-build`) using the **owning user's** credentials injected just-in-time via a git credential-helper that calls back to the Secret Manager (credentials never written to disk in the snapshot).
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
       + repo_pins[url → resolved SHA]     # omitted entries for session-start clones
       + builder_version
       + base_image_patch_level )
```

Same key → reuse snapshot, skip the minutes-long build. Any input change → new key → rebuild async while the current snapshot keeps serving (build-ahead).

## Session Materialize (cold vs warm)

On run start, the Orchestrator requests a sandbox via the operator (§16):

- **Warm hit**: a pooled microVM was pre-forked from the snapshot (hot pool keeps N ready per popular cache key, N driven by forecast + queue depth). Session attaches a fresh writable overlay on top of the snapshot — copy-on-write, so sessions never mutate the shared snapshot. Start latency < 1s.
- **Warm miss**: restore from object-stored FS snapshot onto local NVMe (~2–5s), then boot.
- **Cold**: full environment build (minutes); run is queued with "environment provisioning" status; subsequent runs are warm.

Per-session writes live only in the overlay. On session end the overlay is **always discarded** — durable state is git + transcript + manifest (§16.2). No filesystem checkpointing in v1.

## Repo Handling During Sessions

- Clones land under `/workspace/<repo>` owned by the sandbox user.
- Git operations *inside* the session (push, PR creation) go through the Tool Gateway's git facade — authenticated as the user, policy-checked, audited. The remote URL is rewritten to point at the facade, so raw credentials are never present in the sandbox.
- Large-repo ergonomics: sparse checkout / partial clone flags configurable; LFS passthrough via gateway.

## Lifecycle & Hygiene

- **Invalidation**: snapshots expire (default 7d TTL); base-image CVE patches force rebuild; users can force-refresh (`POST /v1/environments/{id}:rebuild`, §12).
- **Storage economics**: dedupe by chunk-level content addressing (snapshots share blocks across versions); LRU eviction of cold snapshots; per-org snapshot storage quota.
- **Security**: snapshots encrypted per-org KMS key; access scoped to owner user; a snapshot from user A can never be mounted by user B even within the same org, unless explicitly shared via a grant. Secret-scrub scan gates every snapshot publish.
- **Observability**: per-phase metrics (build time, restore time, warm-hit ratio, snapshot size) surfaced per agent/org.

API endpoints: see [12-api.md](12-api.md).
