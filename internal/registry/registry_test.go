//go:build integration

package registry_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/registry"
	"github.com/prav-j/dark-factory/internal/testutil"
)

func newStore(t *testing.T) *registry.Store {
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

	// Seed org + user for FK targets.
	for _, q := range []string{
		`INSERT INTO orgs (id, name) VALUES ('` + orgID + `', 'test-org')
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject)
		 VALUES ('` + userID + `', '` + orgID + `', 'owner@dev.local', 'subj-owner')
		 ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return registry.NewStore(conn)
}

func TestFullVersionLifecycle(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	agent, v1, err := store.CreateAgent(ctx, orgID, userID, "lifecycle-bot", []byte(specV1))
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if v1.Version != 1 || v1.Status != "draft" {
		t.Fatalf("v1 = %+v, want draft version 1", v1)
	}

	// Draft is mutable.
	v1b, err := store.UpdateDraft(ctx, orgID, agent.ID, 1, []byte(specV2))
	if err != nil {
		t.Fatalf("update draft: %v", err)
	}
	if v1b.SpecHash == v1.SpecHash {
		t.Fatal("draft update should change spec hash")
	}

	// Publish -> becomes current version.
	pub, err := store.PublishVersion(ctx, orgID, agent.ID, 1)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Status != "published" || pub.PublishedAt == nil {
		t.Fatalf("published version wrong: %+v", pub)
	}
	got, err := store.GetAgent(ctx, orgID, agent.ID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.CurrentVersion == nil || *got.CurrentVersion != 1 {
		t.Fatalf("current version = %v, want 1", got.CurrentVersion)
	}

	// Mutating a published version must fail at API level...
	if _, err := store.UpdateDraft(ctx, orgID, agent.ID, 1, []byte(specV1)); !errors.Is(err, registry.ErrImmutable) {
		t.Fatalf("mutating published version: got %v, want ErrImmutable", err)
	}

	// ...and at DB level (bypass the service by writing directly).
	var n int
	if err := store.Raw().QueryRowContext(ctx,
		`SELECT count(*) FROM agent_versions WHERE id=$1 AND status='published'`, pub.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Raw().ExecContext(ctx,
		`UPDATE agent_versions SET spec='{"hacked": true}'::jsonb WHERE id=$1`, pub.ID); err == nil {
		t.Fatal("DB trigger must block direct mutation of published versions")
	}

	// New version gets number 2; publish it; current moves forward.
	v2, err := store.AddVersion(ctx, orgID, agent.ID, []byte(specV2))
	if err != nil {
		t.Fatalf("add version: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("new version = %d, want 2", v2.Version)
	}
	if _, err := store.PublishVersion(ctx, orgID, agent.ID, 2); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	got, _ = store.GetAgent(ctx, orgID, agent.ID)
	if got.CurrentVersion == nil || *got.CurrentVersion != 2 {
		t.Fatalf("current version after v2 publish = %v, want 2", got.CurrentVersion)
	}

	// Deprecation is the only post-publish transition; going back to draft is not.
	if _, err := store.DeprecateVersion(ctx, orgID, agent.ID, 1); err != nil {
		t.Fatalf("deprecate v1: %v", err)
	}
	if _, err := store.PublishVersion(ctx, orgID, agent.ID, 1); !errors.Is(err, registry.ErrInvalidState) {
		t.Fatalf("re-publish deprecated: got %v, want ErrInvalidState", err)
	}

	// Version list, newest first.
	versions, err := store.ListVersions(ctx, orgID, agent.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("versions = %+v", versions)
	}

	// Duplicate names rejected within an org.
	if _, _, err := store.CreateAgent(ctx, orgID, userID, "lifecycle-bot", []byte(specV1)); !errors.Is(err, registry.ErrDuplicateName) {
		t.Fatalf("duplicate create: got %v, want ErrDuplicateName", err)
	}

	// Cross-org access returns NotFound (org scoping in every query).
	if _, err := store.GetAgent(ctx, "22222222-2222-2222-2222-222222222222", agent.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("cross-org get: got %v, want ErrNotFound", err)
	}
}
