//go:build integration

package grants_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/grants"
	"github.com/prav-j/dark-factory/internal/testutil"
)

const (
	orgID  = "11111111-1111-1111-1111-111111111111"
	userID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

func newGrantStore(t *testing.T) *grants.Store {
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
		`INSERT INTO orgs (id, name) VALUES ('` + orgID + `', 'test-org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + userID + `', '` + orgID + `', 'u@dev.local', 'subj-u') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &grants.Store{DB: conn}
}

func TestConsentFlowToGrant(t *testing.T) {
	store := newGrantStore(t)
	ctx := context.Background()

	req, err := store.RequestConsent(ctx, orgID, userID, "github/create_issue", "repo:issues:write")
	if err != nil {
		t.Fatalf("request consent: %v", err)
	}

	grant, err := store.DecideConsent(ctx, orgID, req.ID, true, "user-click")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if grant == nil || grant.Scope != "repo:issues:write" {
		t.Fatalf("grant = %+v", grant)
	}

	scopes, err := store.ActiveScopes(ctx, orgID, userID)
	if err != nil || len(scopes) != 1 || scopes[0] != "repo:issues:write" {
		t.Fatalf("active scopes = %v (err %v)", scopes, err)
	}

	// Double-decide is rejected.
	if _, err := store.DecideConsent(ctx, orgID, req.ID, true, "again"); err == nil {
		t.Fatal("re-deciding a request must fail")
	}
}

func TestDenyCreatesNoGrant(t *testing.T) {
	store := newGrantStore(t)
	ctx := context.Background()

	req, _ := store.RequestConsent(ctx, orgID, userID, "r", "s")
	grant, err := store.DecideConsent(ctx, orgID, req.ID, false, "user-click")
	if err != nil || grant != nil {
		t.Fatalf("deny should return nil grant, got %+v err %v", grant, err)
	}
	scopes, _ := store.ActiveScopes(ctx, orgID, userID)
	if len(scopes) != 0 {
		t.Fatalf("denied scope leaked: %v", scopes)
	}
}

// TestRevocationLatency asserts specs/04's <5s revocation guarantee:
// after Revoke + cache invalidation, the next read must not include the
// revoked scope even if the cache TTL has not elapsed.
func TestRevocationLatency(t *testing.T) {
	store := newGrantStore(t)
	ctx := context.Background()

	req, _ := store.RequestConsent(ctx, orgID, userID, "github/create_issue", "repo:issues:write")
	grant, err := store.DecideConsent(ctx, orgID, req.ID, true, "user-click")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	cache := grants.NewScopeCache(store, 30*time.Second) // TTL longer than the test
	if _, err := cache.ActiveScopes(ctx, orgID, userID); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := store.Revoke(ctx, orgID, grant.ID, userID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	cache.Invalidate(orgID, userID)

	var scopes []string
	for {
		scopes, err = cache.ActiveScopes(ctx, orgID, userID)
		if err != nil {
			t.Fatal(err)
		}
		if len(scopes) == 0 {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("revocation not visible within 5s; still %v", scopes)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("revocation took %v", elapsed)
	}
}

func TestExpiryExcludesScopes(t *testing.T) {
	store := newGrantStore(t)
	ctx := context.Background()

	req, _ := store.RequestConsent(ctx, orgID, userID, "r", "temp-scope")
	grant, err := store.DecideConsent(ctx, orgID, req.ID, true, "user-click")
	if err != nil {
		t.Fatal(err)
	}
	// Backdate expiry directly (store API takes no expiry in v1 consent flow).
	if _, err := store.DB.Exec(
		`UPDATE grants SET expiry = now() - interval '1 minute' WHERE id = $1`, grant.ID); err != nil {
		t.Fatal(err)
	}
	scopes, _ := store.ActiveScopes(ctx, orgID, userID)
	if len(scopes) != 0 {
		t.Fatalf("expired scope leaked: %v", scopes)
	}
}
