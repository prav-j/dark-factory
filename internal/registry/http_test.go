//go:build integration

package registry_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// TestHTTPAPIEndToEnd exercises the REST surface over a real Postgres:
// create -> add version -> publish -> mutate-published conflict -> deprecate.
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
		 VALUES ('` + userID + `', '` + orgID + `', 'owner@dev.local', 'subj-owner') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	srv := httptest.NewServer(registry.NewHTTPHandler(registry.NewStore(conn)))
	t.Cleanup(srv.Close)

	post := func(path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-ID", orgID)
		req.Header.Set("X-User-ID", userID)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	ctx := context.Background()

	// Unauthenticated request rejected.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/agents",
		strings.NewReader(`{"name":"x","specYaml":""}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing identity headers: got %d, want 401", resp.StatusCode)
	}

	code, out := post("/v1/agents", `{"name":"http-bot","specYaml":`+jsonString(specV1)+`}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	agent := out["agent"].(map[string]any)
	agentID := agent["id"].(string)

	code, _ = post("/v1/agents/"+agentID+"/versions", `{"specYaml":`+jsonString(specV2)+`}`)
	if code != http.StatusCreated {
		t.Fatalf("add version: %d", code)
	}

	code, _ = post("/v1/agents/"+agentID+"/versions/1:publish", `{}`)
	if code != http.StatusOK {
		t.Fatalf("publish v1: %d", code)
	}
	code, _ = post("/v1/agents/"+agentID+"/versions/2:publish", `{}`)
	if code != http.StatusOK {
		t.Fatalf("publish v2: %d", code)
	}

	// Mutating published v2 conflicts (PUT draft-update on a published version).
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/agents/"+agentID+"/versions/2",
		strings.NewReader(`{"specYaml":`+jsonString(specV1)+`}`))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("X-Org-ID", orgID)
	putReq.Header.Set("X-User-ID", userID)
	resp2, err := srv.Client().Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("mutate published via PUT: %d, want 409", resp2.StatusCode)
	}

	// Deprecate v1.
	code, _ = post("/v1/agents/"+agentID+"/versions/1:deprecate", `{}`)
	if code != http.StatusOK {
		t.Fatalf("deprecate v1: %d", code)
	}

	// Agent reflects current version 2.
	getReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/agents/"+agentID, nil)
	getReq.Header.Set("X-Org-ID", orgID)
	getReq.Header.Set("X-User-ID", userID)
	resp, err = srv.Client().Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CurrentVersion *int `json:"currentVersion"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.CurrentVersion == nil || *got.CurrentVersion != 2 {
		t.Fatalf("currentVersion = %v, want 2", got.CurrentVersion)
	}

	_ = ctx
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
