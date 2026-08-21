//go:build conformance

package conformance

import "testing"

// Spec: specs/04-identity-scoping.md
func TestIdentityScoping(t *testing.T) {
	t.Run("C04-001_delegated_identity", func(t *testing.T) {
		Pending(t, Check{ID: "C04-001", Spec: "04-identity-scoping.md#delegated-identity",
			Text: "Agents are never principals; every external call executes as the owning user via delegated credentials."})
	})
	// C04-002 verified by the container-backed check in 04_token_conformance_test.go.
	// C04-003 verified by the container-backed check in 04_token_conformance_test.go.
	// C04-004 verified by 04_policy_conformance_test.go.
	// C04-005 verified by the container-backed check in 04_token_conformance_test.go.
	t.Run("C04-006_session_sandbox_isolation", func(t *testing.T) {
		Pending(t, Check{ID: "C04-006", Spec: "04-identity-scoping.md#tenant-isolation-layers",
			Text: "One sandbox per session; no shared filesystem between sessions; cross-tenant retrieval impossible."})
	})
}
