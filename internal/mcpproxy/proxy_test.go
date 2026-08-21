package mcpproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/mcpproxy"
	"github.com/prav-j/dark-factory/internal/policy"
)

type fakeSession struct {
	org, user, server string
	tools             []mcpproxy.ToolInfo
	responses         map[string]json.RawMessage
	closed            bool
}

func (f *fakeSession) ListTools(_ context.Context) ([]mcpproxy.ToolInfo, error) {
	return f.tools, nil
}
func (f *fakeSession) CallTool(_ context.Context, tool string, _ json.RawMessage) (json.RawMessage, error) {
	if r, ok := f.responses[tool]; ok {
		return r, nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func (f *fakeSession) Close() error { f.closed = true; return nil }

type fakeDialer struct {
	sessions map[string]*fakeSession // key user|server
	dials    int
}

func (d *fakeDialer) Dial(_ context.Context, org, user, server string) (mcpproxy.Session, error) {
	d.dials++
	key := user + "|" + server
	if s, ok := d.sessions[key]; ok {
		return s, nil
	}
	return nil, errors.New("no credentials registered for " + key)
}

func setup(t *testing.T) (*mcpproxy.Manager, *fakeDialer) {
	t.Helper()
	pol, err := policy.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := pol.SetOrgPatterns([]string{"mcp.call:*"}); err != nil {
		t.Fatal(err)
	}
	dialer := &fakeDialer{sessions: map[string]*fakeSession{
		"alice|github-official": {org: "org-1", user: "alice", server: "github-official", tools: []mcpproxy.ToolInfo{
			{Name: "issues_list"}, {Name: "issues_create"}, {Name: "repos_delete"},
		}},
		"bob|github-official": {org: "org-1", user: "bob", server: "github-official", tools: []mcpproxy.ToolInfo{
			{Name: "issues_list"},
		}},
	}}
	mgr := mcpproxy.NewManager(dialer, pol, 5*time.Second, 1<<20)
	return mgr, dialer
}

func TestPerUserRoutingIsolation(t *testing.T) {
	mgr, dialer := setup(t)
	ctx := context.Background()

	alice := mcpproxy.Authz{OrgID: "org-1", UserID: "alice",
		Grants: []string{"mcp:github-official:issues_list", "mcp:github-official:issues_create"}}
	bob := mcpproxy.Authz{OrgID: "org-1", UserID: "bob",
		Grants: []string{"mcp:github-official:issues_list"}}

	aTools, err := mgr.ExposedTools(ctx, alice, "github-official", []string{"issues_*"})
	if err != nil || len(aTools) != 2 {
		t.Fatalf("alice tools = %v err %v (repos_delete must be hidden by glob)", aTools, err)
	}
	bTools, err := mgr.ExposedTools(ctx, bob, "github-official", []string{"*"})
	if err != nil || len(bTools) != 1 {
		t.Fatalf("bob tools = %v err %v", bTools, err)
	}

	// Bob cannot invoke issues_create even with the glob "*": his connection
	// doesn't expose it AND his grants lack the scope.
	if _, err := mgr.Call(ctx, bob, "github-official",
		mcpproxy.Namespace("github-official", "issues_create"), json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "not exposed") {
		t.Fatalf("bob invoking alice-only tool: %v", err)
	}

	// Sessions are distinct objects per user (no shared cache).
	if dialer.sessions["alice|github-official"] == dialer.sessions["bob|github-official"] {
		t.Fatal("users must not share MCP sessions")
	}

	mgr.CloseAll()
	if !dialer.sessions["alice|github-official"].closed {
		t.Fatal("CloseAll must close sessions")
	}
}

func TestNamespacingAndUnknownTool(t *testing.T) {
	if got := mcpproxy.Namespace("gh", "issues_list"); got != "mcp__gh__issues_list" {
		t.Fatalf("namespace = %q", got)
	}
	srv, tool, ok := mcpproxy.SplitNamespace("mcp__gh__issues_list")
	if !ok || srv != "gh" || tool != "issues_list" {
		t.Fatalf("split = %q %q %v", srv, tool, ok)
	}
}

func TestResponseValidation(t *testing.T) {
	pol, _ := policy.NewEngine()
	_ = pol.SetOrgPatterns([]string{"mcp.call:*"})
	bad := &fakeSession{tools: []mcpproxy.ToolInfo{{Name: "x"}}}
	dialer := &fakeDialer{sessions: map[string]*fakeSession{"u|srv": bad}}
	mgr := mcpproxy.NewManager(dialer, pol, time.Second, 1024)
	bad.responses = map[string]json.RawMessage{"x": json.RawMessage("not-json")}

	authz := mcpproxy.Authz{OrgID: "o", UserID: "u", Grants: []string{"mcp:srv:x"}}
	_, err := mgr.Call(context.Background(), authz, "srv",
		mcpproxy.Namespace("srv", "x"), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("want invalid-response error, got %v", err)
	}
}
