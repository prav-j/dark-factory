//go:build conformance && integration

package conformance

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// C03-004 — Postgres row-level security keyed on org/user blocks cross-org
// reads (specs/03-data-model.md). Container-backed: runs in the CI
// conformance job with -tags=conformance,integration.
func TestC03004RLSCrossTenantReadsFail(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx := context.Background()

	for _, q := range []string{
		`INSERT INTO orgs (id, name) VALUES ('11111111-1111-1111-1111-111111111111', 'org-a'),
		                                          ('22222222-2222-2222-2222-222222222222', 'org-b')`,
		`INSERT INTO users (id, org_id, email, auth_subject) VALUES
		 ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '11111111-1111-1111-1111-111111111111', 'a@dev.local', 'subj-a'),
		 ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', '22222222-2222-2222-2222-222222222222', 'b@dev.local', 'subj-b')`,
		`INSERT INTO agents (id, org_id, owner_user_id, name) VALUES
		 ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01', '11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'bot'),
		 ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb01', '22222222-2222-2222-2222-222222222222', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'bot')`,
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, q := range []string{`RESET ROLE`, `RESET app.org_id`, `SET ROLE darkfactory_app`} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := conn.ExecContext(ctx,
		`SELECT set_config('app.org_id', '11111111-1111-1111-1111-111111111111', false)`); err != nil {
		t.Fatalf("set org: %v", err)
	}

	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM agents`).Scan(&n); err != nil {
		t.Fatalf("count as org A: %v", err)
	}
	if n != 1 {
		t.Fatalf("org A sees %d agents, want 1 (own only)", n)
	}
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM agents WHERE id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb01'`).Scan(&n); err != nil {
		t.Fatalf("cross-org lookup: %v", err)
	}
	if n != 0 {
		t.Fatalf("RLS violated: org A can read org B agent rows")
	}

	Pass(t, Check{ID: "C03-004", Spec: "03-data-model.md#entities-store-split",
		Text: "Postgres row-level security keyed on org/user blocks cross-org reads."})
}
