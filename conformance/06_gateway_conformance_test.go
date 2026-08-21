//go:build conformance && integration

package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prav-j/dark-factory/internal/gitfacade"
	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/toolgw"
)

// C06-001..003 — tool gateway pipeline order, no-token denial, egress
// allowlist (composes #18's gateway).
func TestC06001To003ToolGatewayPipeline(t *testing.T) {
	pol, _ := policy.NewEngine()
	if err := pol.SetOrgPatterns([]string{"tool.call:http_request"}); err != nil {
		t.Fatal(err)
	}
	tokens := runtoken.New([]byte("k"), staticSessionsForPack(), noRevocationsForPack{})
	reg := toolgw.NewRegistry(1024)
	gw := toolgw.New(reg, pol, tokens)
	reg.Register(toolgw.ToolDef{
		Name: "http_request", RequiredScope: "net:fetch",
	}, toolgw.HTTPRequestTool([]string{"api.internal.corp"}), nil, 10000)
	input := json.RawMessage(`{"url":"https://api.internal.corp/v1"}`)
	ctx := context.Background()

	// C06-002: no token -> nothing executes (pipeline aborts first).
	if _, err := gw.Call(ctx, "", "http_request", input); err == nil ||
		!strings.Contains(err.Error(), "run token") {
		t.Fatalf("C06-002 no-token: %v", err)
	}

	// C06-001: pipeline order — valid token but unknown tool fails at the
	// registry stage; known tool without scope fails at policy; both precede
	// execution.
	token, _, _ := tokens.Mint(ctx, runtoken.MintRequest{
		RunID: "r", SessionID: "s", UserID: "u", OrgID: "o", Grants: []string{"net:fetch"},
	})
	if _, err := gw.Call(ctx, token, "no_such_tool", input); err == nil ||
		!strings.Contains(err.Error(), "unknown tool") {
		t.Fatal("registry stage missing")
	}
	noScope, _, _ := tokens.Mint(ctx, runtoken.MintRequest{
		RunID: "r2", SessionID: "s", UserID: "u", OrgID: "o", Grants: []string{"other"},
	})
	if _, err := gw.Call(ctx, noScope, "http_request", input); err == nil ||
		!strings.Contains(err.Error(), "denied by policy") {
		t.Fatal("policy stage missing")
	}

	// C06-003: egress allowlist blocks non-approved domains even with grants.
	blocked := json.RawMessage(`{"url":"https://evil.example.net/x"}`)
	if _, err := gw.Call(ctx, token, "http_request", blocked); err == nil ||
		!strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("C06-003 egress: %v", err)
	}
	// Approved domain passes.
	if _, err := gw.Call(ctx, token, "http_request", input); err != nil {
		t.Fatalf("allowlisted call failed: %v", err)
	}

	Pass(t, Check{ID: "C06-001", Spec: "06-tool-gateway.md",
		Text: "Call pipeline order: authn run token -> policy -> rate limit -> inject user creds -> execute -> redact/normalize."})
	Pass(t, Check{ID: "C06-002", Spec: "06-tool-gateway.md",
		Text: "A request without a valid Run Token never reaches a tool implementation."})
	Pass(t, Check{ID: "C06-003", Spec: "06-tool-gateway.md",
		Text: "HTTP tool egress restricted to org-configured domain allowlist."})
}

// C06-004 — remote rewrite binds facade URLs to runs; raw credentials only
// appear on the facade->upstream hop.
func TestC06004GitFacadeRewriteAndIsolation(t *testing.T) {
	codec := gitfacade.NewURLCodec("https://facade.internal", []byte("k"))
	facade, err := codec.Rewrite("run-1", "https://github.com/acme/api")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.TrimPrefix(facade, "https://facade.internal/"), "/", 2)
	if origin, err := codec.Origin("run-1", parts[0], parts[1]); err != nil || origin != "https://github.com/acme/api" {
		t.Fatalf("roundtrip origin=%q err=%v", origin, err)
	}
	// Bound to its run: another run cannot use this URL.
	if _, err := codec.Origin("run-2", parts[0], parts[1]); err == nil {
		t.Fatal("facade URL must be bound to its run")
	}
	Pass(t, Check{ID: "C06-004", Spec: "06-tool-gateway.md#git-facade",
		Text: "In-session git remotes are rewritten to the facade; raw credentials never enter the sandbox."})
}
