# Testing Strategy

The test suite is the product's proof of correctness: **if CI is green, the system works and complies with `specs/`**. Two mechanisms enforce this:

1. **Conformance suite** (`conformance/`) — one test file per spec area; every normative statement in a spec maps to at least one test. See issue #3.
2. **Layered tests** below — each layer catches a different class of failure.

## Test pyramid

| Layer | Location | Dependencies | Runtime | Purpose |
|---|---|---|---|---|
| Unit | next to code (`*_test.go`) | none — pure logic, fakes injected | < 5s | Business logic, edge cases |
| Integration | build tag `integration` | real Postgres / Redis / LocalStack(DDB) via testcontainers | ~1–2 min | Schema, RLS, migrations, store behavior |
| E2E | build tag `e2e` | full control+exec plane in-process or kind | ~2–5 min | User journeys: create agent → run → resume |

## Commands

```
make test               # unit only (default in pre-commit loop)
make test-integration   # + integration (requires Docker)
make test-e2e           # + e2e
make cover              # coverage report
```

CI runs all three layers on every PR. Coverage gate: packages changed in a PR must keep ≥ 80% coverage (enforced via `cover-check` job).

## Determinism rules

- **No sleeps.** Time is injected via `testutil.Clock`; advance it manually in tests.
- **No real external services.** LLM providers are faked via `testutil.FakeLLM`; OAuth/OIDC via `testutil.FakeOIDC`.
- **Real infrastructure over mocks for state.** Store logic runs against real engines (containers), not hand-rolled mocks — mocks drift from reality.
- Fixed seeds for anything random.

## Harness inventory (`internal/testutil`)

| Helper | Replaces | Notes |
|---|---|---|
| `FakeClock` | `time.Now`, timers | Manual advance; safe for backoff/retry/TTL tests |
| `Postgres(t)` | live PG | testcontainers; applies migrations; unique DB per test |
| `Redis(t)` | live Redis | testcontainers; flushed per test |
| `DynamoDB(t)` | live DDB | LocalStack container (per owner guidance); unique table prefixes |
| `FakeLLM` | model providers | Scripted responses; records prompts for assertions |
| `FakeOIDC` | identity provider | Mints tokens for any user/org; JWKS endpoint |

## Secret hygiene in tests

Tests never use real credentials. Containers use throwaway passwords generated per session; fixtures containing anything secret-shaped must live in `_testdata/` and be scanned by the M8 leak-detection pack.
