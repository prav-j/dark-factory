//go:build conformance

package conformance

import "testing"

// Spec: specs/15-executor-environments.md
func TestExecutorEnvironments(t *testing.T) {
	t.Run("C15-001_snapshot_cache_key_semantics", func(t *testing.T) {
		Pending(t, Check{ID: "C15-001", Spec: "15-executor-environments.md#environment-build-snapshots",
			Text: "Snapshot cache key = H(image_digest + setup hash + repo pins + builder_version + patch level); same key reuses snapshot."})
	})
	t.Run("C15-002_secret_scrub_gates_publish", func(t *testing.T) {
		Pending(t, Check{ID: "C15-002", Spec: "15-executor-environments.md#lifecycle-hygiene",
			Text: "Secret-scrub scan gates every snapshot publish; snapshots encrypted per-org and scoped to owner user."})
	})
	t.Run("C15-003_cow_overlay_never_mutates_snapshot", func(t *testing.T) {
		Pending(t, Check{ID: "C15-003", Spec: "15-executor-environments.md#session-materialize-cold-vs-warm",
			Text: "Sessions attach a fresh COW overlay; the shared snapshot is never mutated; overlays discarded at session end."})
	})
	t.Run("C15-004_builds_outside_execution_plane", func(t *testing.T) {
		Pending(t, Check{ID: "C15-004", Spec: "15-executor-environments.md#image-build-pipeline",
			Text: "Image builds run in isolated BuildKit builders outside the execution plane, network-restricted to declared allowlists."})
	})
}
