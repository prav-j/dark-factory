//go:build conformance

package conformance

import "testing"

// Spec: specs/06-tool-gateway.md
func TestToolGateway(t *testing.T) {
	t.Run("C06-001_pipeline_order", func(t *testing.T) {
		Pending(t, Check{ID: "C06-001", Spec: "06-tool-gateway.md",
			Text: "Call pipeline order: authn run token -> policy -> rate limit -> inject user creds -> execute -> redact/normalize."})
	})
	t.Run("C06-002_no_token_no_execution", func(t *testing.T) {
		Pending(t, Check{ID: "C06-002", Spec: "06-tool-gateway.md",
			Text: "A request without a valid Run Token never reaches a tool implementation."})
	})
	t.Run("C06-003_egress_allowlist_enforced", func(t *testing.T) {
		Pending(t, Check{ID: "C06-003", Spec: "06-tool-gateway.md",
			Text: "HTTP tool egress restricted to org-configured domain allowlist."})
	})
	t.Run("C06-004_git_facade_rewrites_remote", func(t *testing.T) {
		Pending(t, Check{ID: "C06-004", Spec: "06-tool-gateway.md#git-facade",
			Text: "In-session git remotes are rewritten to the facade; raw credentials never enter the sandbox."})
	})
}
