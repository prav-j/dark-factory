# 12 — API Surface (v1, consolidated)

This file is the **single source of truth** for the API. Endpoint fragments in other specs are illustrative; additions must land here.

## Agents

```
POST   /v1/agents                      # create draft
PUT    /v1/agents/{id}/versions        # new version
POST   /v1/agents/{id}:publish
```

## Runs & Sessions (ships with W3 — live scenario wiring)

```
POST   /v1/runs                        # {agent, input, stream: true}
POST   /v1/runs                        # {session: {resume: "sess_..."}}   # resume (§16.4)
GET    /v1/runs/{id}                   # status, usage
POST   /v1/runs/{id}:cancel
POST   /v1/runs/{id}/approvals         # HITL decisions
GET    /v1/sessions/{id}               # session status + manifest
```

> Status: these endpoints are specified but land with the execution-plane
> wiring (roadmap W3.3/W2.8). C12-001 scope = shipped endpoints below until
> then; conformance re-scoped at W3.6.

## Shipped v1 endpoints

```
POST /v1/agents
POST /v1/agents/{id}/versions                     # append next draft version
PUT  /v1/agents/{id}/versions/{n}                 # replace draft spec
GET  /v1/agents/{id}
GET  /v1/agents/{id}/versions
GET  /v1/agents/{id}/versions/{n}
POST /v1/agents/{id}/versions/{n}:publish
POST /v1/agents/{id}/versions/{n}:deprecate
POST /credentials/exchange                        # run-token gated (not OIDC)
GET  /healthz, /readyz
```

## Tools, MCP, Grants, Environments

```
GET    /v1/tools                       # catalog
POST   /v1/mcp/connections             # register MCP server
POST   /v1/grants                      # consent records
POST   /v1/environments                # define/rebuild environment
GET    /v1/environments/{id}           # status: building | ready(snapshot_key)
POST   /v1/environments/{id}:rebuild
GET    /v1/snapshots?key=...           # inspect cache entries
GET    /v1/audit?resource=...
```

CLI + Web UI are thin clients over the same API. Agent specs are portable files (GitOps-friendly).
