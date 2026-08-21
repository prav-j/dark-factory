//go:build conformance

package conformance

import "testing"

// Spec: specs/05-execution-flow.md
func TestExecutionFlow(t *testing.T) {
	t.Run("C05-001_only_granted_tools_exposed", func(t *testing.T) {
		Pending(t, Check{ID: "C05-001", Spec: "05-execution-flow.md#agent-loop-runtime",
			Text: "Context assembly exposes only tools in the effective grant set to the model."})
	})
	t.Run("C05-002_hitl_approval_gates", func(t *testing.T) {
		Pending(t, Check{ID: "C05-002", Spec: "05-execution-flow.md#agent-loop-runtime",
			Text: "Tools marked requiresApproval pause the run pending user decision; run resumes on approve/deny."})
	})
	t.Run("C05-003_git_is_durable_workspace", func(t *testing.T) {
		Pending(t, Check{ID: "C05-003", Spec: "05-execution-flow.md#persistence-model",
			Text: "Durable state is git branches/PRs + transcript + session manifest; sandbox overlays are always discarded."})
	})
	t.Run("C05-004_webhook_idempotency", func(t *testing.T) {
		Pending(t, Check{ID: "C05-004", Spec: "05-execution-flow.md#autonomous-runs-scheduledwebhook",
			Text: "Webhook triggers are idempotent via idempotency keys; budget checked before start."})
	})
}
