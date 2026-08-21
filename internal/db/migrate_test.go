//go:build integration

package db_test

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// openSuperuser returns a connection as the migration/admin role (bypasses
// RLS) with the pgx stdlib driver registered.
func openSuperuser(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestMigrateUpIdempotent(t *testing.T) {
	dsn := testutil.Postgres(t)

	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("second migrate (should be no-op): %v", err)
	}

	conn := openSuperuser(t, dsn)
	var n int
	if err := conn.QueryRow(
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema='public' AND table_name IN
		 ('orgs','users','agents','agent_versions','tool_registry',
		  'mcp_connections','grants','run_records','messages_index','secrets','audit_events')`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 11 {
		t.Fatalf("expected 11 tables after migration, found %d", n)
	}
}
