//go:build conformance

package conformance

import "testing"

// Spec: specs/03-data-model.md
func TestDataModel(t *testing.T) {
	// C03-001 verified by the container-backed check in 03_registry_conformance_test.go.
	t.Run("C03-002_grant_resolves_every_capability", func(t *testing.T) {
		Pending(t, Check{ID: "C03-002", Spec: "03-data-model.md#entities-store-split",
			Text: "Every capability an agent uses resolves through a grant tied to the human user who owns the run."})
	})
	// C03-003 verified by the container-backed check in 03_registry_conformance_test.go.
	// C03-004 (RLS cross-tenant denial) is verified by the container-backed
	// check in 03_rls_conformance_test.go.
}
