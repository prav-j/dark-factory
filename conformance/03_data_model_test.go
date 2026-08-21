//go:build conformance

package conformance

import "testing"

// Spec: specs/03-data-model.md
func TestDataModel(t *testing.T) {
	t.Run("C03-001_immutable_agent_versions", func(t *testing.T) {
		Pending(t, Check{ID: "C03-001", Spec: "03-data-model.md#entities-store-split",
			Text: "Published agent versions are immutable; drafts are mutable; deprecation is the only post-publish transition."})
	})
	t.Run("C03-002_grant_resolves_every_capability", func(t *testing.T) {
		Pending(t, Check{ID: "C03-002", Spec: "03-data-model.md#entities-store-split",
			Text: "Every capability an agent uses resolves through a grant tied to the human user who owns the run."})
	})
	t.Run("C03-003_store_split_no_dual_authority", func(t *testing.T) {
		Pending(t, Check{ID: "C03-003", Spec: "03-data-model.md#entities-store-split",
			Text: "Live run/session state lives in DynamoDB; completed-run lineage and message pointers land in Postgres. No entity is authoritative in both."})
	})
	t.Run("C03-004_rls_cross_tenant_reads_fail", func(t *testing.T) {
		Pending(t, Check{ID: "C03-004", Spec: "03-data-model.md#entities-store-split",
			Text: "Postgres row-level security keyed on org/user blocks cross-org reads."})
	})
}
