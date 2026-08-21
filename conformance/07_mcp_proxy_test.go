//go:build conformance

package conformance

import "testing"

// Spec: specs/07-mcp-proxy.md
func TestMCPProxy(t *testing.T) {
	t.Run("C07-001_per_user_routing_isolation", func(t *testing.T) {
		Pending(t, Check{ID: "C07-001", Spec: "07-mcp-proxy.md",
			Text: "Two users on the same MCP server never share sessions, caches, or credentials."})
	})
	// C07-002 verified by 07_mcp_conformance_test.go.
	// C07-003 verified by 07_mcp_conformance_test.go.
}
