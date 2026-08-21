package environments_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/environments"
)

type memStore struct{ blobs map[string][]byte }

func newStore() *memStore { return &memStore{blobs: map[string][]byte{}} }
func (m *memStore) Put(_ context.Context, d string, b []byte) error {
	m.blobs[d] = b
	return nil
}
func (m *memStore) Get(_ context.Context, d string) ([]byte, bool) {
	b, ok := m.blobs[d]
	return b, ok
}

type countingBuilder struct {
	builds int
	blob   []byte
	fail   error
	delay  time.Duration
}

func (c *countingBuilder) Build(ctx context.Context, _ environments.BuildRequest) ([]byte, error) {
	c.builds++
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.fail != nil {
		return nil, c.fail
	}
	return c.blob, nil
}

func TestIdenticalRebuildIsNoOp(t *testing.T) {
	store := newStore()
	backend := &countingBuilder{blob: []byte("layer-data")}
	svc := environments.NewBuilderService(store,
		environments.StaticScanner{Findings: map[string][]environments.Vulnerability{}},
		nil, backend, time.Minute, 100)

	req := environments.BuildRequest{
		Type: "dockerfile", BaseDigest: "sha256:base",
		Dockerfile: "FROM ubuntu:24.04\nRUN apt-get update",
		BuildArgs:  map[string]string{"VER": "1"},
	}

	d1, err := svc.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := svc.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digests differ: %s vs %s", d1, d2)
	}
	if backend.builds != 1 {
		t.Fatalf("built %d times, want exactly 1 (cache hit on rebuild)", backend.builds)
	}

	// Any input change -> new digest -> real build.
	req.BuildArgs["VER"] = "2"
	if _, err := svc.Acquire(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if backend.builds != 2 {
		t.Fatalf("changed input must trigger a build, got %d", backend.builds)
	}
}

func TestScanGateBlocksCritical(t *testing.T) {
	store := newStore()
	backend := &countingBuilder{blob: []byte("vulnerable")}
	puller := &fakePuller{blob: []byte("pulled-image")}
	findings := map[string][]environments.Vulnerability{}
	svc := environments.NewBuilderService(store,
		environments.StaticScanner{Findings: findings}, puller, backend, time.Minute, 100)

	// Compute the digest the service will derive, then plant a critical CVE.
	digest, _ := environments.BuildRequest{
		Type: "docker-ref", Ref: "docker.io/acme/img:1",
	}.Digest()
	findings[digest] = []environments.Vulnerability{
		{ID: "CVE-2026-0001", Severity: "HIGH"},
		{ID: "CVE-2026-6666", Severity: "CRITICAL"},
	}

	_, err := svc.Acquire(context.Background(), environments.BuildRequest{Type: "docker-ref", Ref: "docker.io/acme/img:1"})
	if !errors.Is(err, environments.ErrScanBlocked) || !strings.Contains(err.Error(), "CVE-2026-6666") {
		t.Fatalf("err = %v, want ErrScanBlocked naming the critical CVE", err)
	}
	if len(store.blobs) != 0 {
		t.Fatal("blocked image must not enter the store")
	}

	// High-only findings pass.
	delete(findings, digest)
	if _, err := svc.Acquire(context.Background(), environments.BuildRequest{Type: "docker-ref", Ref: "docker.io/acme/img:1"}); err != nil {
		t.Fatalf("high-severity only should pass: %v", err)
	}
}

type fakePuller struct{ blob []byte }

func (f *fakePuller) Pull(_ context.Context, _ string, _ string) ([]byte, error) {
	return f.blob, nil
}

func TestBuildTimeoutHonored(t *testing.T) {
	store := newStore()
	backend := &countingBuilder{blob: []byte("slow"), delay: 5 * time.Second}
	svc := environments.NewBuilderService(store,
		environments.StaticScanner{}, nil, backend, 200*time.Millisecond, 100)

	_, err := svc.Acquire(context.Background(), environments.BuildRequest{
		Type: "dockerfile", Dockerfile: "FROM x",
	})
	if !errors.Is(err, environments.ErrBuildTimeout) {
		t.Fatalf("err = %v, want ErrBuildTimeout", err)
	}
}

func TestDigestStableAcrossArgOrder(t *testing.T) {
	a := environments.BuildRequest{Type: "dockerfile", Dockerfile: "D",
		BuildArgs: map[string]string{"A": "1", "B": "2"}}
	b := environments.BuildRequest{Type: "dockerfile", Dockerfile: "D",
		BuildArgs: map[string]string{"B": "2", "A": "1"}}
	da, _ := a.Digest()
	db, _ := b.Digest()
	if da != db {
		t.Fatalf("arg order affected digest: %s vs %s", da, db)
	}
}

func TestInvalidRequests(t *testing.T) {
	for _, req := range []environments.BuildRequest{
		{Type: "dockerfile"},    // no content
		{Type: "docker-ref"},    // no ref
		{Type: "wat", Ref: "x"}, // unknown type
	} {
		if _, err := req.Digest(); !errors.Is(err, environments.ErrInvalidRequest) {
			t.Fatalf("req %+v: got %v, want ErrInvalidRequest", req, err)
		}
	}
}
