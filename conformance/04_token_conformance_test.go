//go:build conformance && integration

package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/grants"
	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/testutil"
	"github.com/prav-j/dark-factory/internal/toolgw"
)

const (
	c04org  = "11111111-1111-1111-1111-111111111111"
	c04user = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

// C04-002 / C04-003 / C04-005 — one continuous flow: consent -> grant ->
// mint -> gateway accepts -> revoke -> gateway denies within seconds;
// tokens capped at session deadline; no token means no egress.
func TestC04002_03_05TokenLifecycle(t *testing.T) {
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
		`INSERT INTO orgs (id, name) VALUES ('` + c04org + `', 'org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + c04user + `', '` + c04org + `', 'u@x', 'subj') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	// Consent -> grant (the only path to a scope).
	gs := &grants.Store{DB: conn}
	req, err := gs.RequestConsent(ctx(), c04org, c04user, "http_request", "net:fetch")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := gs.DecideConsent(ctx(), c04org, req.ID, true, "user")
	if err != nil || grant == nil {
		t.Fatalf("approve: %+v err %v", grant, err)
	}

	pol, _ := policy.NewEngine()
	if err := pol.SetOrgPatterns([]string{"tool.call:http_request"}); err != nil {
		t.Fatal(err)
	}
	sessions := staticSessionsForPackType{}
	revocations := &packRevocations{revoked: map[string]time.Time{}}
	clock := testutil.NewFakeClock(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC))
	tokens := runtoken.New([]byte("k"), sessions, revocations)
	tokens.SetClock(clock)

	reg := toolgw.NewRegistry(4096)
	gw := toolgw.New(reg, pol, tokens)
	scopeCache := grants.NewScopeCache(gs, 30*time.Second) // long TTL proves invalidation
	gw.SetLiveScopes(scopeCache)
	executed := false
	reg.Register(toolgw.ToolDef{
		Name: "http_request", RequiredScope: "net:fetch",
	}, func(_ context.Context, _ json.RawMessage) (string, error) {
		executed = true
		return `{}`, nil
	}, nil, 10000)

	input := json.RawMessage(`{"url":"https://api.internal.corp/v1"}`)

	// C04-002: no token -> nothing executes.
	if _, err := gw.Call(ctx(), "", "http_request", input); err == nil && executed {
		t.Fatal("no-token call must not execute")
	}

	// Mint with a session deadline 5m out; TTL is 15m so expiry must cap.
	sessionDeadline := clock.Now().Add(5 * time.Minute)
	sessionsWithDeadline := deadlineSessions{deadline: sessionDeadline}
	tokens2 := runtoken.New([]byte("k"), sessionsWithDeadline, revocations)
	tokens2.SetClock(clock)
	token, claims, err := tokens2.Mint(ctx(), runtoken.MintRequest{
		RunID: "run-1", SessionID: "sess-1", UserID: c04user, OrgID: c04org,
		Grants: []string{"net:fetch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Unix(claims.ExpiresAt, 0).After(sessionDeadline) {
		t.Fatalf("token outlives session: %v > %v", time.Unix(claims.ExpiresAt, 0), sessionDeadline)
	}

	// Valid token + grant -> executes.
	res, err := gw.Call(ctx(), token, "http_request", input)
	if err != nil || !executed || res == nil {
		t.Fatalf("authorized call failed: %v", err)
	}

	// C04-005: immediate revocation of the underlying grant denies within
	// bound — the gateway reads live scopes; cache invalidation makes the
	// next call authoritative despite the long TTL.
	start := time.Now()
	if err := gs.Revoke(ctx(), c04org, grant.ID, c04user); err != nil {
		t.Fatal(err)
	}
	scopeCache.Invalidate(c04org, c04user)
	for {
		var callErr error
		_, callErr = gw.Call(ctx(), token, "http_request", input)
		if callErr != nil && strings.Contains(callErr.Error(), "consent") {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("revocation not enforced within 5s; last err=%v", callErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	Pass(t, Check{ID: "C04-002", Spec: "04-identity-scoping.md#delegated-identity",
		Text: "Tool Gateway and MCP Proxy validate a Run Token on every call; no token means no egress."})
	Pass(t, Check{ID: "C04-003", Spec: "04-identity-scoping.md#token-lifetime-rule",
		Text: "Run Tokens are renewable by the harness while the parent session is alive but never outlive their run or session."})
	Pass(t, Check{ID: "C04-005", Spec: "04-identity-scoping.md#permission-broker-policy-engine",
		Text: "Revoking a grant blocks gateway calls within seconds."})
}

type deadlineSessions struct{ deadline time.Time }

func (d deadlineSessions) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return runtoken.SessionInfo{Alive: true, Deadline: d.deadline}, nil
}
