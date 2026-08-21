//go:build conformance

package conformance

import "testing"

// Spec: specs/10-observability-audit.md
func TestObservabilityAudit(t *testing.T) {
	t.Run("C10-001_trace_correlation_by_run", func(t *testing.T) {
		Pending(t, Check{ID: "C10-001", Spec: "10-observability-audit.md",
			Text: "Every LLM call, tool call, and MCP call in a run is traced with spans correlated by run_id."})
	})
	t.Run("C10-002_append_only_audit_log", func(t *testing.T) {
		Pending(t, Check{ID: "C10-002", Spec: "10-observability-audit.md",
			Text: "Every authorization decision is recorded to an append-only audit log (who/what/allow-deny/why)."})
	})
}
