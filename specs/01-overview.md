# 01 — Overview

A multi-tenant platform that lets any user define, deploy, and operate AI agents of their choice — each configured with a model, prompt/persona, tools, and MCP (Model Context Protocol) servers — with all access strictly scoped to the owning user's identity and permissions.

Core promise: **"Bring your own agent; we run it safely on your behalf."**

## Goals

- Users define agents declaratively (spec file or UI) — model, system prompt, tools, MCP servers.
- Tools and MCP servers execute **as the user**, scoped to the user's credentials and grants — never the platform's.
- Strong tenant isolation: one user's agent can never touch another's data, secrets, or sessions.
- Managed lifecycle: versioning, deployment, monitoring, rate limiting, audit logs.
- Support both interactive (chat) and autonomous/scheduled (background) execution.

## Non-Goals (v1)

- Training or fine-tuning models.
- Marketplace for sharing agents across tenants (future).
- Self-hosted / BYO-infra execution (future enterprise tier).

## Glossary

| Term | Meaning |
|---|---|
| **Session** | A long-lived unit of work; owns exactly one sandbox. Contains one or more runs. |
| **Run** | One execution of the agent loop within a session (one user message, one scheduled tick, one webhook). |
| **Environment** | Image + setup steps + repo pins; built once and snapshotted (§15). |
| **Snapshot** | Content-addressed filesystem (optionally VM-state) image of a built environment. |
| **Grant** | User-consented permission record; the only path to any capability. |
