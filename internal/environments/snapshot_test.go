package environments_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/environments"
)

type fakeRunner struct {
	runs    int
	fsState []byte
	fail    error
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ string) ([]byte, error) {
	f.runs++
	if f.fail != nil {
		return nil, f.fail
	}
	return f.fsState, nil
}

func baseReq() environments.SnapshotRequest {
	return environments.SnapshotRequest{
		ImageDigest:    "sha256:img",
		SetupSteps:     []string{"apt-get update", "pip install -e ."},
		RepoPins:       map[string]string{"https://github.com/acme/api": "abc123"},
		BuilderVersion: "v1",
		BasePatchLevel: "2026.08",
	}
}

func TestCacheKeySensitivity(t *testing.T) {
	base := baseReq().CacheKey()

	sensitive := []struct {
		name string
		mut  func(*environments.SnapshotRequest)
	}{
		{"image digest", func(r *environments.SnapshotRequest) { r.ImageDigest = "sha256:other" }},
		{"setup step", func(r *environments.SnapshotRequest) { r.SetupSteps = append(r.SetupSteps, "extra") }},
		{"repo pin SHA", func(r *environments.SnapshotRequest) {
			r.RepoPins["https://github.com/acme/api"] = "def456"
		}},
		{"builder version", func(r *environments.SnapshotRequest) { r.BuilderVersion = "v2" }},
		{"patch level", func(r *environments.SnapshotRequest) { r.BasePatchLevel = "2026.09" }},
	}
	for _, tc := range sensitive {
		req := baseReq()
		tc.mut(&req)
		if req.CacheKey() == base {
			t.Fatalf("%s did not change the cache key", tc.name)
		}
	}

	// Setup order must not matter.
	reordered := baseReq()
	reordered.SetupSteps = []string{"pip install -e .", "apt-get update"}
	if reordered.CacheKey() != base {
		t.Fatal("setup ordering must not change the key")
	}
}

func TestSameKeyReusesSnapshot(t *testing.T) {
	store := environments.NewSnapshotStore()
	runner := &fakeRunner{fsState: []byte("fs-with-deps-installed")}
	b := environments.NewEnvironmentBuilder(runner, store)
	ctx := context.Background()
	req := baseReq()

	first, err := b.Build(ctx, req)
	if err != nil || first.FromCache {
		t.Fatalf("first build: %+v err %v", first, err)
	}
	second, err := b.Build(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !second.FromCache || runner.runs != 1 {
		t.Fatalf("second build fromCache=%v runs=%d, want cache hit with 1 run", second.FromCache, runner.runs)
	}
}

func TestSecretScrubBlocksPublish(t *testing.T) {
	store := environments.NewSnapshotStore()
	runner := &fakeRunner{fsState: []byte("config with ghp_leakedtoken inside")}
	b := environments.NewEnvironmentBuilder(runner, store)

	_, err := b.Build(context.Background(), baseReq())
	if !errors.Is(err, environments.ErrSecretInSnapshot) {
		t.Fatalf("err = %v, want ErrSecretInSnapshot", err)
	}
	// Nothing published.
	if _, ok := store.Get(context.Background(), baseReq().CacheKey()); ok {
		t.Fatal("leaky snapshot must not be stored")
	}
}

func TestEvictionTTLAndLRU(t *testing.T) {
	store := environments.NewSnapshotStore()
	runner := &fakeRunner{fsState: []byte("state")}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	b := environments.NewEnvironmentBuilder(runner, store)
	current := now
	b.Clock = func() time.Time { return current }

	old := baseReq()
	old.BuilderVersion = "old"
	fresh := baseReq()
	fresh.BuilderVersion = "fresh"

	if _, err := b.Build(ctx(), old); err != nil { // built at t=0
		t.Fatal(err)
	}
	current = current.Add(time.Hour)
	if _, err := b.Build(ctx(), fresh); err != nil { // built at t=1h
		t.Fatal(err)
	}

	// TTL 30m at t=2h: the old snapshot expires; fresh (touched 1h ago... wait,
	// fresh was touched at t=1h so it is also expired unless re-touched).
	current = current.Add(time.Hour)
	evicted := b.Evict(ctx(), 30*time.Minute, 10)
	if evicted < 2 {
		t.Fatalf("expected both stale entries evicted, got %d", evicted)
	}

	// LRU cap: build three fresh snapshots with distinct versions, cap at 2.
	var keys []string
	for _, v := range []string{"a", "b", "c"} {
		r := baseReq()
		r.BuilderVersion = v
		res, err := b.Build(ctx(), r)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, res.Key)
		current = current.Add(time.Minute) // stagger touch times
		// Re-touch by rebuilding (cache hit updates recency).
		if _, err := b.Build(ctx(), r); err != nil {
			t.Fatal(err)
		}
	}
	evicted = b.Evict(ctx(), time.Hour, 2)
	if evicted != 1 {
		t.Fatalf("LRU cap should evict exactly 1, got %d", evicted)
	}
	// The oldest ("a") must be gone; newest survives.
	if _, ok := store.Get(ctx(), keys[0]); ok {
		t.Fatal("least-recently-used snapshot should have been evicted")
	}
	if _, ok := store.Get(ctx(), keys[2]); !ok {
		t.Fatal("most-recently-used snapshot must survive")
	}
}

func ctx() context.Context { return context.Background() }
