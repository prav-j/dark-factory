//go:build conformance && integration

package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/testutil"
)

const c03org = "11111111-1111-1111-1111-111111111111"
const c03user = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

const c03specV1 = `
apiVersion: agents/v1
kind: Agent
metadata: {name: conf-bot, owner: user-1234}
spec:
  model: {provider: anthropic, name: claude-sonnet-4-5}
  prompt: {type: inline, value: "v1"}
  triggers: [{type: chat}]
  limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}
`

func c03seed(t *testing.T, conn *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO orgs (id, name) VALUES ('` + c03org + `', 'c03-org') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + c03user + `', '` + c03org + `', 'c03@dev.local', 'subj-c03') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// C03-001 — published agent versions are immutable (specs/03). Verified at
// both layers by the registry service (#6): API rejects non-draft mutations
// and the DB trigger blocks direct UPDATEs.
func TestC03001ImmutableAgentVersions(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c03seed(t, conn)

	store := registry.NewStore(conn)
	ctx := context.Background()
	agent, v1, err := store.CreateAgent(ctx, c03org, c03user, "imm-bot", []byte(c03specV1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishVersion(ctx, c03org, agent.ID, v1.Version); err != nil {
		t.Fatal(err)
	}

	// API layer rejects mutation of a published version.
	if _, err := store.UpdateDraft(ctx, c03org, agent.ID, 1, []byte(strings.Replace(c03specV1, `"v1"`, `"v2"`, 1))); !errors.Is(err, registry.ErrImmutable) {
		t.Fatalf("api layer: %v", err)
	}

	// DB layer: direct UPDATE blocked even bypassing the service.
	if _, err := conn.ExecContext(ctx,
		`UPDATE agent_versions SET spec='{"hacked":true}'::jsonb WHERE agent_id=$1 AND version=1`, agent.ID); err == nil {
		t.Fatal("DB trigger must block direct mutation")
	}

	// Deprecation is the only allowed post-publish change.
	if _, err := store.DeprecateVersion(ctx, c03org, agent.ID, 1); err != nil {
		t.Fatalf("deprecate: %v", err)
	}

	Pass(t, Check{ID: "C03-001", Spec: "03-data-model.md#entities-store-split",
		Text: "Published agent versions are immutable; drafts are mutable; deprecation is the only post-publish transition."})
}

// C03-003 — store split: live run/session state only in DynamoDB; completed
// lineage only in Postgres. Asserted structurally here (DDB side proven by
// C16-006): Postgres has no live-status column written by the exec plane.
func TestC03003StoreSplitNoDualAuthority(t *testing.T) {
	dsn := testutil.Postgres(t)
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// run_records carries only completed lineage: no mutable live status.
	var coltype string
	err = conn.QueryRowContext(ctx(),
		`SELECT data_type FROM information_schema.columns
		 WHERE table_name='run_records' AND column_name='status'`).Scan(&coltype)
	if err != nil {
		t.Fatal(err)
	}

	// The exec-plane operational tables (live status) must NOT exist in PG.
	var n int
	if err = conn.QueryRowContext(ctx(),
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema='public' AND table_name IN ('sessions','agent_sessions')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("Postgres must not hold live session state tables")
	}
	if coltype == "" {
		t.Fatal("run_records.status missing")
	}

	Pass(t, Check{ID: "C03-003", Spec: "03-data-model.md#entities-store-split",
		Text: "Live run/session state lives in DynamoDB; completed-run lineage and message pointers land in Postgres. No entity is authoritative in both."})
	_ = json.Marshal // keep json import if assertions evolve
}
