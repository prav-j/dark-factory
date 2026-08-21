//go:build conformance

package conformance

import "testing"

// Spec: specs/11-threat-model.md — mitigations must be executable tests.
func TestThreatModel(t *testing.T) {
	t.Run("C11-001_cross_tenant_isolation_chaos", func(t *testing.T) {
		Pending(t, Check{ID: "C11-001", Spec: "11-threat-model.md",
			Text: "User A can never read user B's data, snapshots, or sessions, even within the same org."})
	})
	t.Run("C11-002_prompt_injection_egress_blocked", func(t *testing.T) {
		Pending(t, Check{ID: "C11-002", Spec: "11-threat-model.md",
			Text: "Untrusted content in context cannot trigger egress outside allowlists."})
	})
	t.Run("C11-003_runaway_agent_guards", func(t *testing.T) {
		Pending(t, Check{ID: "C11-003", Spec: "11-threat-model.md",
			Text: "Step/token/budget caps and the kill switch stop a runaway agent."})
	})
	t.Run("C11-004_stolen_run_token_mitigated", func(t *testing.T) {
		Pending(t, Check{ID: "C11-004", Spec: "11-threat-model.md",
			Text: "A stolen Run Token is bound to session+sandbox, expires in 15m, and is blocked via jti revocation."})
	})
}
