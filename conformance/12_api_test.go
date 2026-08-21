//go:build conformance

package conformance

import "testing"

// Spec: specs/12-api.md
func TestAPISurface(t *testing.T) {
	t.Run("C12-001_api_matches_spec", func(t *testing.T) {
		Pending(t, Check{ID: "C12-001", Spec: "12-api.md",
			Text: "All endpoints in 12-api.md exist with the documented methods and behave as specified; no undocumented mutating endpoints."})
	})
	t.Run("C12-002_unauthenticated_rejected", func(t *testing.T) {
		Pending(t, Check{ID: "C12-002", Spec: "12-api.md",
			Text: "Unauthenticated requests are rejected with 401 on all non-health endpoints."})
	})
}
