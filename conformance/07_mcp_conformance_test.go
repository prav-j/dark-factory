//go:build conformance && integration

package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prav-j/dark-factory/internal/mcpproxy"
	"github.com/prav-j/dark-factory/internal/policy"
)

type c07Session struct {
	tools []mcpproxy.ToolInfo
}

func (f *c07Session) ListTools(_ context.Context) ([]mcpproxy.ToolInfo, error) {
	return f.tools, nil
}
func (f *c07Session) CallTool(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}
func (f *c07Session) Close() error { return nil }

type c07Dialer struct{}

func (d *c07Dialer) Dial(_ context.Context, _, _, _ string) (mcpproxy.Session, error) {
	return &c07Session{tools: []mcpproxy.ToolInfo{
		{Name: "issues_list"}, {Name: "issues_create"}, {Name: "repos_delete"},
	}}, nil
}

// C07-002 — the model sees only allowedTools globs intersected with granted
// scopes; repos_delete is hidden entirely.
func TestC07002ToolFilteringIntersection(t *testing.T) {
	pol, _ := policy.NewEngine()
	if err := pol.SetOrgPatterns([]string{"mcp.call:*"}); err != nil {
		t.Fatal(err)
	}
	mgr := mcpproxy.NewManager(&c07Dialer{}, pol, 5_000_000_000, 1<<20)

	authz := mcpproxy.Authz{OrgID: "o", UserID: "u",
		Grants: []string{"mcp:srv:issues_list", "mcp:srv:issues_create"}}
	tools, err := mgr.ExposedTools(ctx(), authz, "srv", []string{"issues_*"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		if !strings.HasPrefix(tl.Name, "mcp__srv__issues") {
			t.Fatalf("non-matching tool exposed: %q", tl.Name)
		}
	}
	if names[mcpproxy.Namespace("srv", "repos_delete")] || len(names) != 2 {
		t.Fatalf("filtered tools = %v", names)
	}
	Pass(t, Check{ID: "C07-002", Spec: "07-mcp-proxy.md",
		Text: "Model sees only tools in allowedTools intersected with the user's granted scopes."})
}

// C07-003 — collision-safe namespacing; unknown tools rejected.
func TestC07003NamespacedToolIDs(t *testing.T) {
	if got := mcpproxy.Namespace("gh", "issues_list"); got != "mcp__gh__issues_list" {
		t.Fatalf("namespace = %q", got)
	}
	srv, tool, ok := mcpproxy.SplitNamespace("mcp__gh__issues_list")
	if !ok || srv != "gh" || tool != "issues_list" {
		t.Fatalf("split = %q %q %v", srv, tool, ok)
	}
	Pass(t, Check{ID: "C07-003", Spec: "07-mcp-proxy.md",
		Text: "MCP tools are exposed as mcp__<server>__<tool> to avoid collisions."})
}
