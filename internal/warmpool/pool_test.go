package warmpool_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/warmpool"
)

type fakeForker struct {
	mu        sync.Mutex
	forked    []string // envKeys in order
	promoted  map[string]string
	destroyed []string
	failFork  bool
}

func newFakeForker() *fakeForker {
	return &fakeForker{promoted: map[string]string{}}
}

func (f *fakeForker) Fork(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFork {
		return "", errForkFailed
	}
	id := key + "-pod-" + itoa(len(f.forked))
	f.forked = append(f.forked, key)
	return id, nil
}

func (f *fakeForker) Promote(_ context.Context, podID, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promoted[podID] = sessionID
	return nil
}

func (f *fakeForker) Destroy(_ context.Context, podID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.promoted, podID)
	f.destroyed = append(f.destroyed, podID)
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type errStatic string

func (e errStatic) Error() string { return string(e) }

const errForkFailed = errStatic("fork failed")

func TestWarmHitConsumesPooledPod(t *testing.T) {
	f := newFakeForker()
	pool := warmpool.New(f, 4, 30*time.Minute)
	ctx := context.Background()
	const key = "snap-abc"

	if _, err := pool.Replenish(ctx, key, 2); err != nil {
		t.Fatal(err)
	}

	podID, warm, err := pool.Acquire(ctx, key, "sess-1")
	if err != nil || !warm {
		t.Fatalf("warm=%v err=%v", warm, err)
	}
	if got := f.promoted[podID]; got != "sess-1" {
		t.Fatalf("pod %s promoted to %q", podID, got)
	}
	if pool.Size(key) != 1 {
		t.Fatalf("pool size %d after acquiring one", pool.Size(key))
	}
}

func TestColdMissForksOnDemand(t *testing.T) {
	f := newFakeForker()
	pool := warmpool.New(f, 4, 30*time.Minute)

	podID, warm, err := pool.Acquire(context.Background(), "snap-xyz", "sess-9")
	if err != nil || warm {
		t.Fatalf("cold acquire: warm=%v err=%v", warm, err)
	}
	if f.promoted[podID] != "sess-9" {
		t.Fatal("cold fork must also be promoted into the session")
	}
}

func TestReplenishRespectsTargetAndCap(t *testing.T) {
	f := newFakeForker()
	pool := warmpool.New(f, 3, 30*time.Minute)
	ctx := context.Background()

	if n, err := pool.Replenish(ctx, "k", 5); err != nil || n != 3 { // cap 3 < target 5
		t.Fatalf("replenish = %d err %v, want capped at 3", n, err)
	}
	if n, _ := pool.Replenish(ctx, "k", 3); n != 0 {
		t.Fatalf("topped-up pool should need nothing, forked %d", n)
	}
	// After consuming one, replenishing restores the target.
	if _, _, _ = pool.Acquire(ctx, "k", "s"); true {
	}
	if n, _ := pool.Replenish(ctx, "k", 3); n != 1 {
		t.Fatalf("after consumption expected 1 refill, got %d", n)
	}
}

func TestScaleDownDestroysStaleOnly(t *testing.T) {
	f := newFakeForker()
	pool := warmpool.New(f, 4, 15*time.Minute)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	current := now
	pool.SetClock(func() time.Time { return current })
	ctx := context.Background()

	if _, err := pool.Replenish(ctx, "k", 2); err != nil { // both at t=0
		t.Fatal(err)
	}
	current = current.Add(10 * time.Minute)
	if _, err := pool.Replenish(ctx, "k", 3); err != nil { // one more at t=10m
		t.Fatal(err)
	}

	current = current.Add(10 * time.Minute) // t=20m: first two are stale (>=15m), third is not
	destroyed, err := pool.ScaleDown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(destroyed) != 2 {
		t.Fatalf("destroyed %v, want exactly the 2 stale pods", destroyed)
	}
	if pool.Size("k") != 1 {
		t.Fatalf("remaining pool = %d, want 1", pool.Size("k"))
	}
}
