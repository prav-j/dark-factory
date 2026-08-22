# 08 — Secrets Management

- KMS root key per environment; DEKs per user/org (envelope encryption).
- Secrets referenced by ID in agent specs; resolved only inside gateways at call time.
- Short-lived credential exchange where possible (token exchange to downstream providers rather than long-lived PATs).
- Rotation jobs, access logging on every secret read, no secret ever appears in transcripts/logs (scrubbing middleware).
- Git credentials for env builds and session clones are injected just-in-time via credential-helper callbacks to the Secret Manager and are **never written into snapshots** (§15.3).
