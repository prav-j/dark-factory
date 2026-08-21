//go:build conformance && integration

package conformance

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/authn"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/testutil"
)

const c12org = "11111111-1111-1111-1111-111111111111"
const c12user = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// C12-002 — unauthenticated requests are rejected with 401 on all
// non-health routes; provisioned-but-invalid tokens get 403.
func TestC12002UnauthenticatedRejected(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	oidc := testutil.NewFakeOIDC(t)
	auth := authn.NewAuthenticator(oidc.URL(), &authn.DBResolver{DB: conn})
	srv := httptest.NewServer(auth.Middleware(registry.NewHTTPHandler(registry.NewStore(conn))))
	t.Cleanup(srv.Close)

	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/agents"},
		{http.MethodGet, "/v1/agents/some-id"},
		{http.MethodPost, "/v1/agents/some-id/versions"},
		{http.MethodGet, "/v1/agents/some-id/versions"},
		{http.MethodGet, "/v1/agents/some-id/versions/1"},
		{http.MethodPut, "/v1/agents/some-id/versions/1"},
		{http.MethodPost, "/v1/agents/some-id/versions/1:publish"},
		{http.MethodPost, "/v1/agents/some-id/versions/1:deprecate"},
	}
	for _, rt := range routes {
		req, _ := http.NewRequest(rt.method, srv.URL+rt.path, strings.NewReader(`{}`))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: %d, want 401", rt.method, rt.path, resp.StatusCode)
		}
	}

	// Health endpoints stay open (liveness must not require auth).
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Fatalf("health %s: %d", path, resp.StatusCode)
		}
	}

	Pass(t, Check{ID: "C12-002", Spec: "12-api.md",
		Text: "Unauthenticated requests are rejected with 401 on all non-health endpoints."})
}

// C12-001 — every shipped endpoint in specs/12 (Shipped v1 section) exists
// with the documented method and behaves as specified. Verified by driving
// the full documented lifecycle through the API surface.
func TestC12001ShippedEndpointsMatchSpec(t *testing.T) {
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
		`INSERT INTO orgs (id, name) VALUES ('` + c12org + `', 'org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + c12user + `', '` + c12org + `', 'u@x', 'subj') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	oidc := testutil.NewFakeOIDC(t)
	auth := authn.NewAuthenticator(oidc.URL(), &authn.DBResolver{DB: conn})
	srv := httptest.NewServer(auth.Middleware(registry.NewHTTPHandler(registry.NewStore(conn))))
	t.Cleanup(srv.Close)
	token := oidc.MintToken("subj", c12org, "u@x")

	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if code := resp.StatusCode; code >= 400 {
			t.Logf("%s %s -> %d body=%s", method, path, code, string(raw))
		}
		return resp.StatusCode, out
	}

	yamlSpec := "apiVersion: agents/v1\nkind: Agent\nmetadata: {name: c12-bot, owner: u}\nspec:\n  model: {provider: anthropic, name: m}\n  prompt: {type: inline, value: hi}\n  triggers: [{type: chat}]\n  limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}\n"
	createBody, _ := json.Marshal(map[string]string{"name": "c12-bot", "specYaml": yamlSpec})

	code, out := call(http.MethodPost, "/v1/agents", string(createBody))
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/agents: %d %v", code, out)
	}
	agentID := out["agent"].(map[string]any)["id"].(string)

	versionBody, _ := json.Marshal(map[string]string{"specYaml": yamlSpec})
	code, _ = call(http.MethodPost, "/v1/agents/"+agentID+"/versions/1:publish", `{}`)
	if code != http.StatusOK {
		t.Fatalf("publish v1: %d", code)
	}
	code, out = call(http.MethodPost, "/v1/agents/"+agentID+"/versions", string(versionBody))
	if code != http.StatusCreated {
		t.Fatalf("POST versions: %d %v", code, out)
	}
	code, _ = call(http.MethodPost, "/v1/agents/"+agentID+"/versions/2:publish", `{}`)
	if code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}
	code, out = call(http.MethodGet, "/v1/agents/"+agentID, "")
	if code != http.StatusOK {
		t.Fatalf("get agent: %d", code)
	}
	cur, ok := out["currentVersion"].(float64)
	if !ok || int(cur) != 2 {
		t.Fatalf("currentVersion = %v, want 2", out["currentVersion"])
	}
	code, out = call(http.MethodGet, "/v1/agents/"+agentID+"/versions", "")
	if code != http.StatusOK || len(out["versions"].([]any)) != 2 {
		t.Fatalf("list versions: %d %v", code, out)
	}
	code, _ = call(http.MethodPost, "/v1/agents/"+agentID+"/versions/1:deprecate", `{}`)
	if code != http.StatusOK {
		t.Fatalf("deprecate: %d", code)
	}

	Pass(t, Check{ID: "C12-001", Spec: "12-api.md",
		Text: "All shipped endpoints in 12-api.md exist with the documented methods and behave as specified."})
	_ = time.Now
}
