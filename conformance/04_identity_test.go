//go:build conformance

package conformance

import "testing"

// Spec: specs/04-identity-scoping.md
func TestIdentityScoping(t *testing.T) {
	t.Run("C04-001_delegated_identity", func(t *testing.T) {
		Pending(t, Check{ID: "C04-001", Spec: "04-identity-scoping.md#delegated-identity",
			Text: "Agents are never principals; every external call executes as the owning user via delegated credentials."})
	})
	t.Run("C04-002_run_token_required_for_egress", func(t *testing.T) {
		Pending(t, Check{ID: "C04-002", Spec: "04-identity-scoping.md#delegated-identity",
			Text: "Tool Gateway and MCP Proxy validate a Run Token on every call; no token means no egress."})
	})
	t.Run("C04-003_token_never_outlives_run_or_session", func(t *testing.T) {
		Pending(t, Check{ID: "C04-003", Spec: "04-identity-scoping.md#token-lifetime-rule",
			Text: "Run Tokens are renewable by the harness while the parent session is alive but never outlive their run or session."})
	})
	t.Run("C04-004_effective_scope_intersection", func(t *testing.T) {
		Pending(t, Check{ID: "C04-004", Spec: "04-identity-scoping.md#permission-broker-policy-engine",
			Text: "Effective scope = intersection of org policy, user consents, and agent spec scopes. Deny-by-default."})
	})
	t.Run("C04-005_immediate_revocation", func(t *testing.T) {
		Pending(t, Check{ID: "C04-005", Spec: "04-identity-scoping.md#permission-broker-policy-engine",
			Text: "Revoking a grant blocks gateway calls within seconds."})
	})
	t.Run("C04-006_session_sandbox_isolation", func(t *testing.T) {
		Pending(t, Check{ID: "C04-006", Spec: "04-identity-scoping.md#tenant-isolation-layers",
			Text: "One sandbox per session; no shared filesystem between sessions; cross-tenant retrieval impossible."})
	})
}
