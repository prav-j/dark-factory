//go:build integration

package registry_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/modelgw"
	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/testutil"
	"github.com/prav-j/dark-factory/internal/toolgw"
)

// TestInternalGatewayEndToEnd drives the sandbox-facing surface exactly as
// the harness binary does: complete -> tool call, all gated by one run token.
func TestInternalGatewayEndToEnd(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, q := range []string{
		`INSERT INTO orgs (id, name) VALUES ('` + orgID + `', 'org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + userID + `', '` + orgID + `', 'u@x', 'subj') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	pol, _ := policy.NewEngine()
	if err := pol.SetOrgPatterns([]string{"tool.call:http_request", "credential.read:*"}); err != nil {
		t.Fatal(err)
	}
	sessions := staticAlive{deadline: time.Now().Add(time.Hour)}
	tokens := runtoken.New([]byte("k"), sessions, nopRevocations{})
	reg := toolgw.NewRegistry(4096)
	gw := toolgw.New(reg, pol, tokens)
	reg.Register(toolgw.ToolDef{
		Name: "http_request", RequiredScope: "net:fetch",
	}, toolgw.HTTPRequestTool([]string{"api.internal.corp"}), nil, 10000)

	internal := &registry.InternalGateway{
		Tokens: tokens,
		Model: registry.NewScriptedCompleter([]modelgw.CompletionResponse{
			{Content: "hello from model", StopReason: "end_turn"},
		}),
		Tools: gw,
	}
	mux := http.NewServeMux()
	internal.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	token, _, err := tokens.Mint(context.Background(), runtoken.MintRequest{
		RunID: "run-i", SessionID: "sess-1", UserID: userID, OrgID: orgID,
		Grants: []string{"net:fetch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string, body map[string]any) (int, string) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf strings.Builder
		tmp := make([]byte, 512)
		for {
			n, e := resp.Body.Read(tmp)
			buf.Write(tmp[:n])
			if e != nil {
				break
			}
		}
		return resp.StatusCode, buf.String()
	}

	// No token -> 401 on internal routes.
	bad := strings.Replace(token, ".", "_", 1)
	noTokReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/llm/complete",
		strings.NewReader(`{"model":"m"}`))
	noTokReq.Header.Set("Authorization", "Bearer "+bad)
	resp, err := srv.Client().Do(noTokReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("llm bad-token: %d, want 401", resp.StatusCode)
	}

	// LLM completion.
	llmBody := map[string]any{
		"model": "claude-sonnet-4-5", "runId": "run-i", "agent": "bot@v1",
		"userId": userID, "orgId": orgID,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	code, body := post("/internal/llm/complete", llmBody)
	if code != http.StatusOK || !strings.Contains(body, "hello from model") {
		t.Fatalf("llm complete: %d %s", code, body)
	}

	// Tool call through the gateway pipeline.
	toolBody := map[string]any{
		"runToken": token,
		"tool":     "http_request",
		"input":    json.RawMessage(`{"url":"https://api.internal.corp/v1"}`),
	}
	code, body = post("/internal/tools/call", toolBody)
	if code != http.StatusOK || !strings.Contains(body, "would-fetch") {
		t.Fatalf("tool call: %d %s", code, body)
	}
}

type staticAlive struct{ deadline time.Time }

func (s staticAlive) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return runtoken.SessionInfo{Alive: true, Deadline: s.deadline}, nil
}

type nopRevocations struct{}

func (nopRevocations) Revoke(_ context.Context, _ string, _ time.Duration) error { return nil }
func (nopRevocations) IsRevoked(_ context.Context, _ string) (bool, error)       { return false, nil }
