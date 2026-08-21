//go:build integration

package registry_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/authn"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// TestHTTPAPIEndToEnd drives the authenticated REST surface over real
// Postgres + a fake OIDC provider:
// authenticate -> create -> publish -> mutate-published conflict -> deprecate.
func TestHTTPAPIEndToEnd(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, q := range []string{
		`INSERT INTO orgs (id, name) VALUES ('` + orgID + `', 'test-org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + userID + `', '` + orgID + `', 'owner@dev.local', 'alice') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	oidc := testutil.NewFakeOIDC(t)
	auth := authn.NewAuthenticator(oidc.URL(), &authn.DBResolver{DB: conn})
	srv := httptest.NewServer(auth.Middleware(registry.NewHTTPHandler(registry.NewStore(conn))))
	t.Cleanup(srv.Close)

	token := oidc.MintToken("alice", orgID, "owner@dev.local")
	stranger := oidc.MintToken("mallory", orgID, "mallory@dev.local")

	do := func(method, path, tokenStr, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tokenStr != "" {
			req.Header.Set("Authorization", "Bearer "+tokenStr)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// No token -> 401.
	code, _ := do(http.MethodPost, "/v1/agents", "", `{"name":"x","specYaml":""}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", code)
	}

	// Garbage token -> 401.
	code, _ = do(http.MethodPost, "/v1/agents", "not.a.jwt", `{"name":"x","specYaml":""}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("garbage token: got %d, want 401", code)
	}

	// Valid token but unprovisioned subject -> 403.
	code, _ = do(http.MethodPost, "/v1/agents", stranger, `{"name":"x","specYaml":`+jsonString(specV1)+`}`)
	if code != http.StatusForbidden {
		t.Fatalf("unprovisioned subject: got %d, want 403", code)
	}

	// Happy path.
	code, out := do(http.MethodPost, "/v1/agents", token, `{"name":"http-bot","specYaml":`+jsonString(specV1)+`}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	agentID := out["agent"].(map[string]any)["id"].(string)

	code, _ = do(http.MethodPost, "/v1/agents/"+agentID+"/versions", token, `{"specYaml":`+jsonString(specV2)+`}`)
	if code != http.StatusCreated {
		t.Fatalf("add version: %d", code)
	}
	code, _ = do(http.MethodPost, "/v1/agents/"+agentID+"/versions/1:publish", token, `{}`)
	if code != http.StatusOK {
		t.Fatalf("publish v1: %d", code)
	}
	code, _ = do(http.MethodPost, "/v1/agents/"+agentID+"/versions/2:publish", token, `{}`)
	if code != http.StatusOK {
		t.Fatalf("publish v2: %d", code)
	}

	// Mutating published v2 conflicts.
	code, _ = do(http.MethodPut, "/v1/agents/"+agentID+"/versions/2", token, `{"specYaml":`+jsonString(specV1)+`}`)
	if code != http.StatusConflict {
		t.Fatalf("mutate published via PUT: %d, want 409", code)
	}

	// Deprecate v1.
	code, _ = do(http.MethodPost, "/v1/agents/"+agentID+"/versions/1:deprecate", token, `{}`)
	if code != http.StatusOK {
		t.Fatalf("deprecate v1: %d", code)
	}

	// Agent reflects current version 2.
	code, out = do(http.MethodGet, "/v1/agents/"+agentID, token, "")
	if code != http.StatusOK {
		t.Fatalf("get agent: %d", code)
	}
	cur, ok := out["currentVersion"].(float64)
	if !ok || int(cur) != 2 {
		t.Fatalf("currentVersion = %v, want 2", out["currentVersion"])
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
