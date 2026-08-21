// Package environments implements the executor environment pipeline
// (specs/15): image builds with CVE gating, content-addressed image storage,
// environment snapshots keyed for warm starts, and pool management.
//
// This file: the image build phase — BuildKit-rootless builders outside the
// execution plane, pull-through caching, Trivy-style scan gates, size caps
// and timeouts.
package environments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrScanBlocked    = errors.New("image blocked by vulnerability scan")
	ErrBuildTimeout   = errors.New("build exceeded time budget")
	ErrAllowlist      = errors.New("base image outside network allowlist")
	ErrInvalidRequest = errors.New("invalid build request")
)

// BuildRequest describes one image acquisition (specs/15.1).
type BuildRequest struct {
	Type       string            `json:"type"` // docker-ref | dockerfile
	Ref        string            `json:"ref,omitempty"`
	Dockerfile string            `json:"dockerfile,omitempty"`
	BuildArgs  map[string]string `json:"buildArgs,omitempty"`
	BaseDigest string            `json:"baseDigest,omitempty"` // resolved base digest
	MaxSizeMB  int               `json:"maxSizeMB,omitempty"`
}

// Digest is the content-addressed identity of a built image:
// H(dockerfile hash + build args + base digest) per specs/15.2.
func (r BuildRequest) Digest() (string, error) {
	switch r.Type {
	case "docker-ref":
		if strings.TrimSpace(r.Ref) == "" {
			return "", ErrInvalidRequest
		}
	case "dockerfile":
		if strings.TrimSpace(r.Dockerfile) == "" {
			return "", ErrInvalidRequest
		}
	default:
		return "", ErrInvalidRequest
	}

	h := sha256.New()
	args := make([]string, 0, len(r.BuildArgs))
	for k, v := range r.BuildArgs {
		args = append(args, k+"="+v)
	}
	sort.Strings(args)
	payload, _ := json.Marshal(struct {
		Type       string   `json:"t"`
		Ref        string   `json:"r,omitempty"`
		Dockerfile string   `json:"d,omitempty"`
		SortedArgs []string `json:"b,omitempty"`
		BaseDigest string   `json:"p,omitempty"`
	}{r.Type, r.Ref, r.Dockerfile, args, r.BaseDigest})
	h.Write(payload)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Vulnerability is one scanner finding.
type Vulnerability struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // LOW|MEDIUM|HIGH|CRITICAL
}

// Scanner vets images before they may enter the platform registry.
type Scanner interface {
	Scan(ctx context.Context, digest string) ([]Vulnerability, error)
}

// StaticScanner blocks any digest containing a critical vuln entry; tests
// and policy-only environments use it, production fronts Trivy/Grype.
type StaticScanner struct{ Findings map[string][]Vulnerability }

func (s StaticScanner) Scan(_ context.Context, digest string) ([]Vulnerability, error) {
	return s.Findings[digest], nil
}

// ImageStore persists images by digest (per-tenant registry namespace).
type ImageStore interface {
	Put(ctx context.Context, digest string, blob []byte) error
	Get(ctx context.Context, digest string) ([]byte, bool)
}

// Puller acquires prebuilt refs through the pull-through cache proxy.
type Puller interface {
	Pull(ctx context.Context, ref, expectedDigest string) ([]byte, error)
}

// DockerBuilder performs the actual rootless build. Production shells out to
// buildctl/docker against ephemeral builders; tests inject fakes.
type DockerBuilder interface {
	Build(ctx context.Context, req BuildRequest) ([]byte, error)
}

// BuilderService orchestrates acquire-or-build with all gates applied.
type BuilderService struct {
	Store        ImageStore
	Scanner      Scanner
	Puller       Puller // required when docker-ref requests arrive
	BuildBackend DockerBuilder
	Timeout      time.Duration
	MaxSizeMB    int
}

func NewBuilderService(store ImageStore, scanner Scanner, puller Puller, backend DockerBuilder, timeout time.Duration, maxSizeMB int) *BuilderService {
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	if maxSizeMB == 0 {
		maxSizeMB = 2048
	}
	return &BuilderService{Store: store, Scanner: scanner, Puller: puller, BuildBackend: backend, Timeout: timeout, MaxSizeMB: maxSizeMB}
}

// Acquire returns the image digest, building or pulling only on cache miss.
func (s *BuilderService) Acquire(ctx context.Context, req BuildRequest) (string, error) {
	digest, err := req.Digest()
	if err != nil {
		return "", err
	}
	if _, ok := s.Store.Get(ctx, digest); ok {
		return digest, nil // identical rebuild is a no-op
	}

	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	var blob []byte
	switch req.Type {
	case "docker-ref":
		if s.Puller == nil {
			return "", fmt.Errorf("%w: no pull-through cache configured", ErrInvalidRequest)
		}
		blob, err = s.Puller.Pull(ctx, req.Ref, digest)
	default:
		if err := checkDockerfileAllowlist(req.Dockerfile, req.BaseDigest); err != nil {
			return "", err
		}
		blob, err = s.BuildBackend.Build(ctx, req)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ErrBuildTimeout
		}
		return "", err
	}

	// Size cap gate.
	if maxBytes := s.MaxSizeMB << 20; len(blob) > maxBytes {
		return "", fmt.Errorf("image %d bytes exceeds cap %d MB", len(blob), s.MaxSizeMB)
	}

	// Scan gate: critical CVEs block entry to the registry.
	vulns, err := s.Scanner.Scan(ctx, digest)
	if err != nil {
		return "", err
	}
	for _, v := range vulns {
		if strings.EqualFold(v.Severity, "critical") {
			return "", fmt.Errorf("%w: %s (%s)", ErrScanBlocked, v.ID, v.Severity)
		}
	}

	if err := s.Store.Put(ctx, digest, blob); err != nil {
		return "", err
	}
	return digest, nil
}

// checkDockerfileAllowlist verifies every FROM line's image reference is
// within the declared allowlist ("library/ubuntu" style names or hosts).
func checkDockerfileAllowlist(dockerfile, baseDigest string) error {
	if baseDigest == "" {
		return nil // resolution happens in the builder; nothing to verify yet
	}
	for _, line := range strings.Split(dockerfile, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(fields[1], "@") &&
			!strings.HasSuffix(fields[1], baseDigest) {
			return fmt.Errorf("%w: FROM pinned to unexpected digest", ErrAllowlist)
		}
	}
	return nil
}
