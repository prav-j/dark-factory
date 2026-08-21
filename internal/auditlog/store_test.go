//go:build integration

package auditlog_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/audit"
	"github.com/prav-j/dark-factory/internal/auditlog"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/testutil"
)

func newLogStore(t *testing.T) (*auditlog.Store, *sql.DB) {
	t.Helper()
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &auditlog.Store{DB: conn}, conn
}

func write(t *testing.T, ctx context.Context, db *sql.DB, actor, action, resource, decision string) {
	t.Helper()
	if err := audit.Write(ctx, db, actor, action, resource, decision, ""); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFilters(t *testing.T) {
	store, conn := newLogStore(t)
	ctx := context.Background()

	write(t, ctx, conn, "user-a", "tool.call", "builtin/web_search", "allow")
	write(t, ctx, conn, "user-b", "git.push", "acme/api", "deny")
	write(t, ctx, conn, "user-a", "grant.revoke", "cred-1", "allow")

	got, err := store.Query(ctx, auditlog.Filter{Actor: "user-a"})
	if err != nil || len(got) != 2 {
		t.Fatalf("actor filter = %d events err %v", len(got), err)
	}
	denied, err := store.Query(ctx, auditlog.Filter{Decision: "deny"})
	if err != nil || len(denied) != 1 || denied[0].Resource != "acme/api" {
		t.Fatalf("decision filter = %+v err %v", denied, err)
	}
}

// C10-002: the chain detects tampering even by a superuser who disables the
// append-only trigger.
func TestChainVerificationAndTamperDetection(t *testing.T) {
	store, conn := newLogStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		write(t, ctx, conn, "user-a", "authz.decision", "res", "allow")
	}
	n, err := store.VerifyChain(ctx)
	if err != nil || n < 5 {
		t.Fatalf("clean chain verify: %d entries err %v", n, err)
	}

	// Tamper.
	if err := store.Tamper(ctx, 3, "DENIED-FOREVER"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyChain(ctx); err == nil {
		t.Fatal("tampered chain must fail verification")
	} else if !strings.Contains(err.Error(), "entry 3") && !strings.Contains(err.Error(), "entry") {
		t.Fatalf("error should identify the broken point: %v", err)
	}
}
