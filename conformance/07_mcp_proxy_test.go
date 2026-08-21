//go:build conformance

package conformance

import "testing"

// Spec: specs/07-mcp-proxy.md
func TestMCPProxy(t *testing.T) {
	t.Run("C07-001_per_user_routing_isolation", func(t *testing.T) {
		Pending(t, Check{ID: "C07-001", Spec: "07-mcp-proxy.md",
			Text: "Two users on the same MCP server never share sessions, caches, or credentials."})
	})
	t.Run("C07-002_tool_filtering_intersection", func(t *testing.T) {
		Pending(t, Check{ID: "C07-002", Spec: "07-mcp-proxy.md",
			Text: "Model sees only tools in allowedTools intersected with the user's granted scopes."})
	})
	t.Run("C07-003_namespaced_tool_ids", func(t *testing.T) {
		Pending(t, Check{ID: "C07-003", Spec: "07-mcp-proxy.md",
			Text: "MCP tools are exposed as mcp__<server>__<tool> to avoid collisions."})
	})
}
