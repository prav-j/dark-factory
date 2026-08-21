//go:build conformance && integration

package conformance

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/audit"
	"github.com/prav-j/dark-factory/internal/auditlog"
	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// C10-002 — append-only audit log records every authorization decision, and
// the hash chain detects tampering even by a superuser who disables the
// append-only trigger (specs/10).
func TestC10002AppendOnlyAuditLog(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := audit.Write(ctx, conn, "user-a", "authz.decision", "res-x", "allow", ""); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	store := &auditlog.Store{DB: conn}
	got, err := store.Query(ctx, auditlog.Filter{Resource: "res-x", Decision: "allow"})
	if err != nil || len(got) != 3 {
		t.Fatalf("decision query = %d events err %v", len(got), err)
	}
	if n, err := store.VerifyChain(ctx); err != nil || n < 3 {
		t.Fatalf("chain verify: %d err %v", n, err)
	}

	// Tamper must be detected.
	if err := store.Tamper(ctx, got[0].ID, "DENIED"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyChain(ctx); err == nil {
		t.Fatal("tampered chain must fail verification")
	}

	Pass(t, Check{ID: "C10-002", Spec: "10-observability-audit.md",
		Text: "Every authorization decision is recorded to an append-only audit log (who/what/allow-deny/why); tampering is detected via hash chain."})
}
