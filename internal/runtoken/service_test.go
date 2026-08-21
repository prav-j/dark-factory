package runtoken

import (
	"context"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/testutil"
)

type fakeSessions struct {
	sessions map[string]SessionInfo
}

func (f *fakeSessions) GetSession(_ context.Context, id string) (SessionInfo, error) {
	info, ok := f.sessions[id]
	if !ok {
		return SessionInfo{}, errTestNoSession
	}
	return info, nil
}

var errTestNoSession = errStatic("no such session")

type errStatic string

func (e errStatic) Error() string { return string(e) }

type memRevocations struct {
	revoked map[string]time.Time
	clock   testutil.Clock
}

func (m *memRevocations) Revoke(_ context.Context, jti string, ttl time.Duration) error {
	m.revoked[jti] = m.clock.Now().Add(ttl)
	return nil
}

func (m *memRevocations) IsRevoked(_ context.Context, jti string) (bool, error) {
	until, ok := m.revoked[jti]
	return ok && m.clock.Now().Before(until), nil
}

func newTestService(t *testing.T, sessions map[string]SessionInfo) (*Service, *memRevocations, *testutil.FakeClock) {
	t.Helper()
	clock := testutil.NewFakeClock(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC))
	rev := &memRevocations{revoked: map[string]time.Time{}, clock: clock}
	svc := New([]byte("test-secret"), &fakeSessions{sessions: sessions}, rev)
	svc.SetClock(clock)
	return svc, rev, clock
}

func TestMintAndValidate(t *testing.T) {
	svc, _, _ := newTestService(t, map[string]SessionInfo{
		"sess-1": {Alive: true, Deadline: time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)},
	})
	token, claims, err := svc.Mint(context.Background(), MintRequest{
		RunID: "run-1", SessionID: "sess-1", Agent: "bot@v1",
		UserID: "user-1", OrgID: "org-1", Grants: []string{"g"},
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if claims.ExpiresAt-claims.IssuedAt != int64(15*time.Minute/time.Second) {
		t.Fatalf("TTL = %ds, want 900s", claims.ExpiresAt-claims.IssuedAt)
	}
	got, err := svc.Validate(context.Background(), token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.RunID != "run-1" || got.JTI != claims.JTI {
		t.Fatalf("claims mismatch: %+v", got)
	}
}

// C04-003: a token can never outlive its session. The mint is capped at the
// session deadline even though the TTL would extend further.
func TestTokenNeverOutlivesSession(t *testing.T) {
	deadline := time.Date(2026, 8, 21, 12, 5, 0, 0, time.UTC) // 5m away; TTL is 15m
	svc, _, _ := newTestService(t, map[string]SessionInfo{
		"sess-1": {Alive: true, Deadline: deadline},
	})
	_, claims, err := svc.Mint(context.Background(), MintRequest{RunID: "r", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Unix(claims.ExpiresAt, 0); !got.Equal(deadline) {
		t.Fatalf("expiry = %v, want capped at session deadline %v", got, deadline)
	}
}

func TestRenewalRejectedAfterSessionEnds(t *testing.T) {
	sessions := map[string]SessionInfo{
		"sess-1": {Alive: true, Deadline: time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)},
	}
	svc, _, _ := newTestService(t, sessions)
	ctx := context.Background()
	token, _, err := svc.Mint(ctx, MintRequest{RunID: "r", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}

	sessions["sess-1"] = SessionInfo{Alive: false} // session terminated
	if _, _, err := svc.Renew(ctx, token); err == nil {
		t.Fatal("renewal after termination must fail")
	}
}

func TestRenewalCappedAtSessionDeadline(t *testing.T) {
	deadline := time.Date(2026, 8, 21, 12, 10, 0, 0, time.UTC)
	svc, _, clock := newTestService(t, map[string]SessionInfo{
		"sess-1": {Alive: true, Deadline: deadline},
	})
	ctx := context.Background()
	token, _, err := svc.Mint(ctx, MintRequest{RunID: "r", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(8 * time.Minute) // past first expiry; renewal must still cap at deadline
	renewed, claims, err := svc.Renew(ctx, token)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got := time.Unix(claims.ExpiresAt, 0); !got.Equal(deadline) {
		t.Fatalf("renewed expiry = %v, want %v", got, deadline)
	}
	if _, err := svc.Validate(ctx, renewed); err != nil {
		t.Fatalf("renewed token invalid: %v", err)
	}

	// Past the deadline even renewal is impossible.
	clock.Advance(3 * time.Minute)
	if _, _, err := svc.Renew(ctx, token); err == nil {
		t.Fatal("renewal past session deadline must fail")
	}
}

// Stolen-token scenario: revoke by jti; the same token (and any renewal of
// it) is denied at validation.
func TestRevokedTokenDenied(t *testing.T) {
	svc, _, _ := newTestService(t, map[string]SessionInfo{
		"sess-1": {Alive: true, Deadline: time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)},
	})
	ctx := context.Background()
	token, claims, err := svc.Mint(ctx, MintRequest{RunID: "r", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Validate(ctx, token); err == nil {
		t.Fatal("revoked token must not validate")
	}
	_ = claims

	// Tampered signature denied too.
	svc2, _, _ := newTestService(t, map[string]SessionInfo{
		"sess-1": {Alive: true, Deadline: time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)},
	})
	tok2, _, _ := svc2.Mint(ctx, MintRequest{RunID: "r", SessionID: "sess-1"})
	forged := tok2[:len(tok2)-2] + "xx"
	if _, err := svc2.Validate(ctx, forged); err == nil {
		t.Fatal("forged token must not validate")
	}
}
