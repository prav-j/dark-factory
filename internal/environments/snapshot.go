package environments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrSecretInSnapshot = errors.New("snapshot publish blocked: secret material detected")
)

// SnapshotRequest captures every input the environment build depends on.
// The cache key is H over ALL of them per specs/15.3.
type SnapshotRequest struct {
	ImageDigest    string            `json:"imageDigest"`
	SetupSteps     []string          `json:"setupSteps"`
	RepoPins       map[string]string `json:"repoPins"` // url -> resolved SHA (env-build clones only)
	BuilderVersion string            `json:"builderVersion"`
	BasePatchLevel string            `json:"basePatchLevel"`
}

// CacheKey is the content address of a built environment snapshot.
func (r SnapshotRequest) CacheKey() string {
	setup := make([]string, len(r.SetupSteps))
	copy(setup, r.SetupSteps)
	sort.Strings(setup)

	pins := make([]string, 0, len(r.RepoPins))
	for url, sha := range r.RepoPins {
		pins = append(pins, url+"@"+sha)
	}
	sort.Strings(pins)

	h := sha256.New()
	h.Write([]byte(r.ImageDigest))
	for _, s := range setup {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	for _, p := range pins {
		h.Write([]byte{1})
		h.Write([]byte(p))
	}
	h.Write([]byte{2})
	h.Write([]byte(r.BuilderVersion))
	h.Write([]byte(r.BasePatchLevel))
	return "snap-" + hex.EncodeToString(h.Sum(nil))
}

// SandboxRunner executes setup inside a throwaway microVM and returns the
// resulting filesystem state id (btrfs send-stream / overlay tarball).
type SandboxRunner interface {
	Run(ctx context.Context, imageDigest string, script string) (fsState []byte, err error)
}

// SecretScrubber inspects filesystem state for credential-shaped content;
// detection blocks snapshot publish (specs/15.6).
type SecretScrubber func(fsState []byte) error

// DefaultScrubber flags common token shapes; extend with org policy.
func DefaultScrubber(fsState []byte) error {
	s := string(fsState)
	for _, pattern := range []string{"ghp_", "gho_", "xoxb-", "AKIA", "-----BEGIN RSA PRIVATE KEY-----"} {
		if containsStr(s, pattern) {
			return fmt.Errorf("%w: matched %q", ErrSecretInSnapshot, pattern)
		}
	}
	return nil
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// SnapshotStore persists snapshots by cache key (object store in prod).
type SnapshotStore struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{blobs: map[string][]byte{}}
}

func (s *SnapshotStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[key] = data
	return nil
}

func (s *SnapshotStore) Get(_ context.Context, key string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.blobs[key]
	return b, ok
}

func (s *SnapshotStore) Delete(_ context.Context, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, key)
}

// EnvironmentBuilder builds and manages environment snapshots.
type EnvironmentBuilder struct {
	Runner    SandboxRunner
	Scrub     SecretScrubber
	Store     *SnapshotStore
	Clock     func() time.Time
	buildsMu  sync.Mutex
	lastBuilt map[string]time.Time
}

func NewEnvironmentBuilder(runner SandboxRunner, store *SnapshotStore) *EnvironmentBuilder {
	return &EnvironmentBuilder{
		Runner: runner, Scrub: DefaultScrubber, Store: store,
		Clock: time.Now, lastBuilt: map[string]time.Time{},
	}
}

// BuildResult reports which path served the request.
type BuildResult struct {
	Key       string
	Snapshot  []byte
	FromCache bool
}

// Build ensures a snapshot exists for the request's cache key. Same key =>
// reuse without touching the runner (build-ahead callers just re-invoke).
func (b *EnvironmentBuilder) Build(ctx context.Context, req SnapshotRequest) (*BuildResult, error) {
	key := req.CacheKey()
	if snap, ok := b.Store.Get(ctx, key); ok {
		b.noteBuild(key)
		return &BuildResult{Key: key, Snapshot: snap, FromCache: true}, nil
	}

	script, err := json.Marshal(req.SetupSteps)
	if err != nil {
		return nil, err
	}
	fsState, err := b.Runner.Run(ctx, req.ImageDigest, string(script))
	if err != nil {
		return nil, err
	}
	if b.Scrub != nil {
		if err := b.Scrub(fsState); err != nil {
			return nil, err // never publish a leaky snapshot
		}
	}
	if err := b.Store.Put(ctx, key, fsState); err != nil {
		return nil, err
	}
	b.noteBuild(key)
	return &BuildResult{Key: key, Snapshot: fsState}, nil
}

// Rebuild force-refreshes a snapshot (POST :rebuild).
func (b *EnvironmentBuilder) Rebuild(ctx context.Context, req SnapshotRequest) (*BuildResult, error) {
	b.Store.Delete(ctx, req.CacheKey())
	return b.Build(ctx, req)
}

func (b *EnvironmentBuilder) noteBuild(key string) {
	b.buildsMu.Lock()
	defer b.buildsMu.Unlock()
	if b.lastBuilt == nil {
		b.lastBuilt = map[string]time.Time{}
	}
	b.lastBuilt[key] = b.Clock()
}

// Evict enforces TTL and an LRU-ish cap: expired keys go first, then the
// oldest-touched entries until count <= maxEntries.
func (b *EnvironmentBuilder) Evict(ctx context.Context, ttl time.Duration, maxEntries int) int {
	b.buildsMu.Lock()
	defer b.buildsMu.Unlock()
	now := b.Clock()

	type entry struct {
		key   string
		touch time.Time
	}
	var live []entry
	evicted := 0
	for key, touched := range b.lastBuilt {
		if now.Sub(touched) >= ttl {
			b.Store.Delete(ctx, key)
			delete(b.lastBuilt, key)
			evicted++
			continue
		}
		live = append(live, entry{key, touched})
	}
	sort.Slice(live, func(i, j int) bool { return live[i].touch.Before(live[j].touch) })
	for i := 0; i < len(live)-maxEntries; i++ {
		b.Store.Delete(ctx, live[i].key)
		delete(b.lastBuilt, live[i].key)
		evicted++
	}
	return evicted
}
