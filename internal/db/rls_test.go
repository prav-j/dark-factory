//go:build integration

package db_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/testutil"
)

// TestRLSCrossOrgDenied verifies tenant isolation (specs/03, C03-004):
// a session scoped to org A cannot see or write org B rows, and an unset
// app.org_id denies everything (deny-by-default).
func TestRLSCrossOrgDenied(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	admin := openSuperuser(t, dsn)
	ctx := context.Background()

	// Seed two orgs, one user + one agent each.
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

	orgA := "11111111-1111-1111-1111-111111111111"
	agentB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbb01"

	t.Run("org_scoped_session_sees_only_own_rows", func(t *testing.T) {
		c := appSession(t, admin, orgA)
		var n int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM agents`).Scan(&n); err != nil {
			t.Fatalf("count agents as org A: %v", err)
		}
		if n != 1 {
			t.Fatalf("org A sees %d agents, want 1", n)
		}
	})

	t.Run("cross_org_row_lookup_returns_nothing", func(t *testing.T) {
		c := appSession(t, admin, orgA)
		var n int
		if err := c.QueryRowContext(ctx,
			`SELECT count(*) FROM agents WHERE id = $1`, agentB).Scan(&n); err != nil {
			t.Fatalf("lookup org B agent: %v", err)
		}
		if n != 0 {
			t.Fatalf("org A must not see org B agent, saw %d", n)
		}
	})

	t.Run("cross_org_insert_blocked", func(t *testing.T) {
		c := appSession(t, admin, orgA)
		if _, err := c.ExecContext(ctx,
			`INSERT INTO agents (id, org_id, owner_user_id, name)
			 VALUES (gen_random_uuid(), '22222222-2222-2222-2222-222222222222',
			         'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'evil')`,
		); err == nil {
			t.Fatal("insert into org B must be blocked by RLS")
		}
	})

	t.Run("unset_org_denies_everything", func(t *testing.T) {
		c := appSession(t, admin, "") // no app.org_id set
		var n int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM agents`).Scan(&n); err != nil {
			t.Fatalf("count with unset org: %v", err)
		}
		if n != 0 {
			t.Fatalf("unset org must see nothing, saw %d", n)
		}
	})

	t.Run("audit_log_is_append_only", func(t *testing.T) {
		c := appSession(t, admin, orgA)
		if _, err := c.ExecContext(ctx,
			`INSERT INTO audit_events (actor, action, resource, decision)
			 VALUES ('user-a', 'tool.call', 'builtin/web_search', 'allow')`,
		); err != nil {
			t.Fatalf("audit insert: %v", err)
		}
		if _, err := c.ExecContext(ctx, `UPDATE audit_events SET decision='deny'`); err == nil {
			t.Fatal("audit UPDATE must be blocked")
		}
		if _, err := c.ExecContext(ctx, `DELETE FROM audit_events`); err == nil {
			t.Fatal("audit DELETE must be blocked")
		}
	})
}

// appSession returns a connection acting as the restricted application role
// with the given org bound via app.org_id. Empty org leaves it unset.
func appSession(t *testing.T, admin *sql.DB, orgID string) *sql.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// The pool may hand us a connection with leftover session state from a
	// previous subtest; reset both role and org binding before scoping.
	for _, q := range []string{`RESET ROLE`, `RESET app.org_id`} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `SET ROLE darkfactory_app`); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if orgID != "" {
		// Session-scoped (false), since statements here run outside an
		// explicit transaction; transaction-local would evaporate instantly.
		if _, err := conn.ExecContext(ctx, `SELECT set_config('app.org_id', $1, false)`, orgID); err != nil {
			t.Fatalf("set org: %v", err)
		}
	}
	return conn
}
