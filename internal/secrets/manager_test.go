//go:build integration

package secrets_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prav-j/dark-factory/internal/db"
	"github.com/prav-j/dark-factory/internal/secrets"
	"github.com/prav-j/dark-factory/internal/testutil"
)

const (
	orgA  = "11111111-1111-1111-1111-111111111111"
	orgB  = "22222222-2222-2222-2222-222222222222"
	userA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	userB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func newManager(t *testing.T, rootKey []byte) *secrets.Manager {
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
		`INSERT INTO orgs (id, name) VALUES ('` + orgA + `', 'a'), ('` + orgB + `', 'b') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, org_id, email, auth_subject) VALUES
		 ('` + userA + `', '` + orgA + `', 'a@dev.local', 'sa'),
		 ('` + userB + `', '` + orgB + `', 'b@dev.local', 'sb') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &secrets.Manager{
		DB:         conn,
		RootKeys:   secrets.StaticRootKeys{Keys: map[int][]byte{1: rootKey}},
		KEKVersion: 1,
	}
}

func TestPutGetRoundtrip(t *testing.T) {
	m := newManager(t, []byte("root-key-a-root-key-a-root-key-a"))
	ctx := context.Background()

	id, err := m.Put(ctx, orgA, userA, []byte("gh_pat=supersecret"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := m.Get(ctx, orgA, userA, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "gh_pat=supersecret" {
		t.Fatalf("roundtrip mismatch: %q", got)
	}

	if err := m.Delete(ctx, orgA, userA, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get(ctx, orgA, userA, id); err == nil {
		t.Fatal("get after delete must fail")
	}
}

func TestWrongTenantDenied(t *testing.T) {
	m := newManager(t, []byte("root-key-a-root-key-a-root-key-a"))
	ctx := context.Background()
	id, _ := m.Put(ctx, orgA, userA, []byte("private"))

	if _, err := m.Get(ctx, orgA, userB, id); err == nil {
		t.Fatal("user B must not read user A's secret")
	}
	if _, err := m.Get(ctx, orgB, userB, id); err == nil {
		t.Fatal("cross-org read must fail")
	}
}

func TestCiphertextUnreadableWithoutRootKey(t *testing.T) {
	// Store under root key A...
	mA := newManager(t, []byte("root-key-a-root-key-a-root-key-a"))
	ctx := context.Background()
	id, err := mA.Put(ctx, orgA, userA, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}

	// ...then open the same DB with a different environment root key.
	dsn := testutil.Postgres(t) // fresh DB would lose data; reuse via manager swap instead
	_ = dsn
	mB := &secrets.Manager{
		DB:         mA.DB,
		RootKeys:   secrets.StaticRootKeys{Keys: map[int][]byte{1: []byte("root-key-b-root-key-b-root")}},
		KEKVersion: 1,
	}
	if _, err := mB.Get(ctx, orgA, userA, id); err == nil {
		t.Fatal("ciphertext must be unreadable without the original KEK")
	}
}

func TestEveryReadIsAudited(t *testing.T) {
	m := newManager(t, []byte("root-key-a-root-key-a-root-key-a"))
	ctx := context.Background()
	id, _ := m.Put(ctx, orgA, userA, []byte("x"))

	countAudits := func() int {
		var n int
		if err := m.DB.QueryRow(
			`SELECT count(*) FROM audit_events WHERE resource=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	before := countAudits() // put already audited
	if _, err := m.Get(ctx, orgA, userA, id); err != nil {
		t.Fatal(err)
	}
	if after := countAudits(); after != before+1 {
		t.Fatalf("expected one audit event per read: before=%d after=%d", before, after)
	}

	// Denied reads are audited too.
	_, _ = m.Get(ctx, orgA, userB, id)
	if after := countAudits(); after != before+2 {
		t.Fatalf("denied read must also be audited: before=%d after=%d", before, after)
	}
}

func TestRotationReencryptsAndKeepsReadability(t *testing.T) {
	m := newManager(t, []byte("root-key-a-root-key-a-root-key-a"))
	ctx := context.Background()

	id1, _ := m.Put(ctx, orgA, userA, []byte("one"))
	id2, _ := m.Put(ctx, orgB, userB, []byte("two")) // different org; untouched

	var ctBefore []byte
	if err := m.DB.QueryRow(`SELECT ciphertext FROM secrets WHERE id=$1`, id1).Scan(&ctBefore); err != nil {
		t.Fatal(err)
	}

	newVer, err := m.RotateOrgDEK(ctx, orgA, "admin")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newVer != 2 {
		t.Fatalf("new DEK version = %d, want 2", newVer)
	}

	var ctAfter []byte
	if err := m.DB.QueryRow(`SELECT ciphertext FROM secrets WHERE id=$1`, id1).Scan(&ctAfter); err != nil {
		t.Fatal(err)
	}
	if string(ctBefore) == string(ctAfter) {
		t.Fatal("rotation must re-encrypt ciphertexts")
	}

	got, err := m.Get(ctx, orgA, userA, id1)
	if err != nil || string(got) != "one" {
		t.Fatalf("post-rotation read: %q err %v", got, err)
	}
	if got, err = m.Get(ctx, orgB, userB, id2); err != nil || string(got) != "two" {
		t.Fatalf("other-org secret disturbed by rotation: %q err %v", got, err)
	}
}
