package toolgw_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/toolgw"
)

const (
	orgID  = "org-1"
	userID = "user-1"
)

type staticSessions struct{ info runtoken.SessionInfo }

func (s staticSessions) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return s.info, nil
}

type noRevocations struct{}

func (noRevocations) Revoke(_ context.Context, _ string, _ time.Duration) error { return nil }
func (noRevocations) IsRevoked(_ context.Context, _ string) (bool, error)       { return false, nil }

type fixture struct {
	gw     *toolgw.Gateway
	tokens *runtoken.Service
	reg    *toolgw.Registry
}

func setup(t *testing.T, patterns []string) *fixture {
	t.Helper()
	pol, err := policy.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := pol.SetOrgPatterns(patterns); err != nil {
		t.Fatal(err)
	}
	sessions := staticSessions{info: runtoken.SessionInfo{
		Alive: true, Deadline: time.Now().Add(time.Hour),
	}}
	tokens := runtoken.New([]byte("secret"), sessions, noRevocations{})
	reg := toolgw.NewRegistry(1024)
	gw := toolgw.New(reg, pol, tokens)
	return &fixture{gw: gw, tokens: tokens, reg: reg}
}

func (f *fixture) mint(grants ...string) string {
	token, _, err := f.tokens.Mint(context.Background(), runtoken.MintRequest{
		RunID: "run-1", SessionID: "sess-1", Agent: "bot@v1",
		UserID: userID, OrgID: orgID, Grants: grants,
	})
	if err != nil {
		panic(err)
	}
	return token
}

func TestPipelineOrderAndDenials(t *testing.T) {
	f := setup(t, []string{"tool.call:http_request", "tool.call:builtin/web_search"})
	f.reg.Register(toolgw.ToolDef{
		Name: "http_request", Description: "fetch url",
		InputSchema: `{"type":"object"}`, RequiredScope: "net:fetch",
	}, toolgw.HTTPRequestTool([]string{"api.example.com"}), nil, 1000)
	f.reg.Register(toolgw.ToolDef{
		Name: "web_search", Description: "search",
		InputSchema: `{"type":"object"}`, RequiredScope: "web:read",
	}, toolgw.WebSearchTool(nil), nil, 1000)

	ctx := context.Background()
	input := json.RawMessage(`{"url":"https://api.example.com/v1/data"}`)

	// 1. No token -> denied before the tool exists check even matters.
	if _, err := f.gw.Call(ctx, "", "http_request", input); err == nil ||
		!strings.Contains(err.Error(), "run token") {
		t.Fatalf("no token: %v", err)
	}

	// 2. Valid token but unknown tool.
	if _, err := f.gw.Call(ctx, f.mint("net:fetch"), "nope", input); err == nil ||
		!strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool: %v", err)
	}

	// 3. Known tool but scope missing from grants -> policy deny.
	if _, err := f.gw.Call(ctx, f.mint("wrong:scope"), "http_request", input); err == nil ||
		!strings.Contains(err.Error(), "denied by policy") {
		t.Fatalf("policy deny: %v", err)
	}

	// 4. Full grants -> allowed; allowlisted host passes.
	res, err := f.gw.Call(ctx, f.mint("net:fetch"), "http_request", input)
	if err != nil || !strings.Contains(res.Output, "would-fetch") {
		t.Fatalf("happy path: %+v err %v", res, err)
	}

	// 5. Non-allowlisted host blocked at egress.
	blocked, err := f.gw.Call(ctx, f.mint("net:fetch"), "http_request",
		json.RawMessage(`{"url":"https://evil.example.net/exfil"}`))
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("egress block: %+v err %v", blocked, err)
	}
}

func TestResponseFiltersAndSizeCap(t *testing.T) {
	f := setup(t, []string{"tool.call:*"})
	called := false
	f.reg.Register(toolgw.ToolDef{Name: "noisy"}, func(_ context.Context, _ json.RawMessage) (string, error) {
		return "SECRET_TOKEN=abc123 " + strings.Repeat("x", 2000), nil
	}, nil, 1000)
	f.reg.AddFilter(func(out string) string {
		called = true
		return strings.ReplaceAll(out, "SECRET_TOKEN=abc123", "[REDACTED]")
	})

	res, err := f.gw.Call(context.Background(), f.mint(), "noisy", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("filter never ran")
	}
	if strings.Contains(res.Output, "SECRET_TOKEN") {
		t.Fatal("DLP filter did not redact")
	}
	if len(res.Output) > 1100 || !strings.Contains(res.Output, "[truncated]") {
		t.Fatalf("size cap not applied: %d bytes", len(res.Output))
	}
}

func TestCatalogForContextAssembly(t *testing.T) {
	f := setup(t, []string{"tool.call:*"})
	f.reg.Register(toolgw.ToolDef{Name: "a"}, func(context.Context, json.RawMessage) (string, error) { return "", nil }, nil, 10)
	cat := f.reg.Catalog()
	if len(cat) != 1 || cat[0].Name != "a" {
		t.Fatalf("catalog = %+v", cat)
	}
}
