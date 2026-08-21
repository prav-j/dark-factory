# dark-factory

Managed agents platform: anyone can define an agent of their choice with access
to tools and MCP, scoped strictly to their own identity. See `specs/` (local)
for the system design.

## Quick start

```
make dev-up      # Postgres + Redis + LocalStack(DDB) + mock OIDC (dev-only creds)
make ci          # vet + lint + build + unit tests
make test        # unit tests (-race)
make test-integration  # container-backed tests (requires Docker)
go run ./cmd/registry      # control-plane registry service on :8080
go run ./cmd/orchestrator  # execution-plane orchestrator service on :8081
```

Mint a dev identity token: `curl 'http://localhost:8082/token?user=alice&org=org-dev'`

## Spec conformance

The `conformance/` suite maps every normative spec statement to a check.
Checks start **PENDING** and flip to real assertions as features land.
**CI green = complies with spec.**

| Spec section | Check file | Status |
|---|---|---|
| 03 data model | [conformance/03_data_model_test.go](conformance/03_data_model_test.go), [03_rls_conformance_test.go](conformance/03_rls_conformance_test.go), [03_registry_conformance_test.go](conformance/03_registry_conformance_test.go) | 3 passed, 1 pending |
| 04 identity & scoping | [conformance/04_identity_test.go](conformance/04_identity_test.go), [04_policy_conformance_test.go](conformance/04_policy_conformance_test.go), [04_token_conformance_test.go](conformance/04_token_conformance_test.go) | 4 passed, 2 pending |
| 05 execution flow | [conformance/05_execution_test.go](conformance/05_execution_test.go), [05_execution_conformance_test.go](conformance/05_execution_conformance_test.go) | 4 passed, 0 pending |
| 06 tool gateway | [conformance/06_tool_gateway_test.go](conformance/06_tool_gateway_test.go), [06_gateway_conformance_test.go](conformance/06_gateway_conformance_test.go) | 4 passed, 0 pending |
| 07 MCP proxy | [conformance/07_mcp_proxy_test.go](conformance/07_mcp_proxy_test.go) | pending (3 checks) |
| 08 secrets | [conformance/08_secrets_test.go](conformance/08_secrets_test.go) | pending (3 checks) |
| 09 scaling & cost | [conformance/09_scaling_cost_test.go](conformance/09_scaling_cost_test.go) | pending (3 checks) |
| 10 observability & audit | [conformance/10_observability_test.go](conformance/10_observability_test.go), [10_audit_conformance_test.go](conformance/10_audit_conformance_test.go) | 1 passed, 1 pending |
| 11 threat model | [conformance/11_threat_model_test.go](conformance/11_threat_model_test.go), [11_security_pack_test.go](conformance/11_security_pack_test.go) | 4 passed, 0 pending |
| 12 API surface | [conformance/12_api_test.go](conformance/12_api_test.go) | pending (2 checks) |
| 15 executor environments | [conformance/15_executor_test.go](conformance/15_executor_test.go) | pending (4 checks) |
| 16 deployment & sessions | [conformance/16_sessions_test.go](conformance/16_sessions_test.go), [16_ddb_conformance_test.go](conformance/16_ddb_conformance_test.go) | 1 passed, 5 pending |

Run locally:

```
CONFORMANCE_REPORT=report.json go test -tags=conformance ./conformance/... -v
```
