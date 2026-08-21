//go:build integration

package credexchange_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/credexchange"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/grants"
	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/secrets"
	"github.com/prav-j/dark-factory/internal/testutil"
)

const (
	orgID  = "11111111-1111-1111-1111-111111111111"
	userID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

type staticSessions struct{ info runtoken.SessionInfo }

func (s staticSessions) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return s.info, nil
}

type noRevocations struct{}

func (noRevocations) Revoke(_ context.Context, _ string, _ time.Duration) error { return nil }
func (noRevocations) IsRevoked(_ context.Context, _ string) (bool, error)       { return false, nil }

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
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
		`INSERT INTO orgs (id, name) VALUES ('` + orgID + `', 'org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + userID + `', '` + orgID + `', 'u@dev.local', 'subj') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	mgr := &secrets.Manager{
		DB:         conn,
		RootKeys:   secrets.StaticRootKeys{Keys: map[int][]byte{1: []byte("test-root-key-test-root-key!")}},
		KEKVersion: 1,
	}
	secretID, err := mgr.Put(context.Background(), orgID, userID, []byte("ghp_devcredential123"))
	if err != nil {
		t.Fatalf("put secret: %v", err)
	}

	// User consents to credentials:read.
	gs := &grants.Store{DB: conn}
	req, err := gs.RequestConsent(context.Background(), orgID, userID, "credentials", "credentials:read")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gs.DecideConsent(context.Background(), orgID, req.ID, true, "user"); err != nil {
		t.Fatal(err)
	}

	pol, err := policy.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := pol.SetOrgPatterns([]string{"credential.read:*"}); err != nil {
		t.Fatal(err)
	}

	tokens := runtoken.New([]byte("token-secret"), staticSessions{
		info: runtoken.SessionInfo{
			Alive:    true,
			Deadline: time.Now().Add(time.Hour),
		},
	}, noRevocations{})

	svc := &credexchange.Service{Tokens: tokens, Secret: mgr, Policy: pol}
	srv := httptest.NewServer(credexchange.Handler(svc))
	t.Cleanup(srv.Close)
	return srv, secretID
}

func TestExchangeHappyPathOverHTTP(t *testing.T) {
	srv, secretID := setup(t)

	tokens := runtoken.New([]byte("token-secret"), staticSessions{
		info: runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)},
	}, noRevocations{})
	token, _, err := tokens.Mint(context.Background(), runtoken.MintRequest{
		RunID: "run-1", SessionID: "sess-1", Agent: "bot@v1",
		UserID: userID, OrgID: orgID, Grants: []string{"credentials:read"},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := srv.Client().Post(srv.URL+"/credentials/exchange", "application/json",
		strings.NewReader(`{"runToken":`+jsonString(token)+`,"credentialRef":`+jsonString(secretID)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exchange: %d", resp.StatusCode)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Secret != "ghp_devcredential123" {
		t.Fatalf("exchanged secret = %q", out.Secret)
	}
}

func TestExchangeRejectsUnauthorizedScopes(t *testing.T) {
	srv, secretID := setup(t)

	tokens := runtoken.New([]byte("token-secret"), staticSessions{
		info: runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)},
	}, noRevocations{})

	// Token without credentials:read grant -> policy denies -> 403.
	token, _, _ := tokens.Mint(context.Background(), runtoken.MintRequest{
		RunID: "run-2", SessionID: "sess-1", UserID: userID, OrgID: orgID,
	})
	resp, err := srv.Client().Post(srv.URL+"/credentials/exchange", "application/json",
		strings.NewReader(`{"runToken":`+jsonString(token)+`,"credentialRef":`+jsonString(secretID)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized exchange: %d, want 403", resp.StatusCode)
	}

	// Garbage token -> 403 as well.
	resp2, err := srv.Client().Post(srv.URL+"/credentials/exchange", "application/json",
		strings.NewReader(`{"runToken":"junk","credentialRef":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("garbage token: %d, want 403", resp2.StatusCode)
	}
}

func TestGitCredentialHelperProtocol(t *testing.T) {
	srv, secretID := setup(t)

	tokens := runtoken.New([]byte("token-secret"), staticSessions{
		info: runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)},
	}, noRevocations{})
	token, _, _ := tokens.Mint(context.Background(), runtoken.MintRequest{
		RunID: "run-3", SessionID: "sess-1", UserID: userID, OrgID: orgID,
		Grants: []string{"credentials:read"},
	})

	cfg := credexchange.HelperConfig{
		ExchangeURL:   srv.URL + "/credentials/exchange",
		RunToken:      token,
		CredentialRef: secretID,
	}
	var stdout bytes.Buffer
	err := credexchange.GitCredentialGet(context.Background(), cfg,
		strings.NewReader("protocol=https\nhost=github.com\n\n"), &stdout)
	if err != nil {
		t.Fatalf("helper: %v", err)
	}

	got := map[string]string{}
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "=", 2)
		if len(parts) == 2 {
			got[parts[0]] = parts[1]
		}
	}
	if got["password"] != "ghp_devcredential123" || got["username"] == "" {
		t.Fatalf("helper output = %v", got)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
