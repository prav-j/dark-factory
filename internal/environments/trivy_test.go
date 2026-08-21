package environments_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/environments"
)

const trivyFixture = `{"Results":[{"Target":"x","Vulnerabilities":[
  {"VulnerabilityID":"CVE-2026-1111","Severity":"HIGH"},
  {"VulnerabilityID":"CVE-2026-2222","Severity":"CRITICAL"}]}]}`

// writeFakeTrivy emits a script behaving like the trivy CLI: prints the
// fixture JSON, exits with --exit-code semantics when requested.
func writeFakeTrivy(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trivy")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do [ \"$a\" = \"--fail\" ] && exit 1; done\n" +
		"echo '" + fixture + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrivyScannerParsesFindings(t *testing.T) {
	scan := &environments.TrivyScanner{Binary: writeFakeTrivy(t, trivyFixture)}
	vulns, err := scan.Scan(context.Background(), "sha256:x")
	if err != nil {
		t.Fatal(err)
	}
	var critical int
	for _, v := range vulns {
		if strings.EqualFold(v.Severity, "CRITICAL") {
			critical++
		}
	}
	if critical != 1 || len(vulns) != 2 {
		t.Fatalf("findings = %+v", vulns)
	}

	// Through the builder gate: critical blocks.
	store := newStore()
	backend := &countingBuilder{blob: []byte("img")}
	svc := environments.NewBuilderService(store, scan, nil, backend, time.Minute, 100)
	digest, _ := environments.BuildRequest{
		Type: "dockerfile", Dockerfile: "FROM x", BaseDigest: "sha256:base",
	}.Digest()
	_ = digest
	if _, err := svc.Acquire(context.Background(), environments.BuildRequest{
		Type: "dockerfile", Dockerfile: "FROM x",
	}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("builder gate should block on critical finding: %v", err)
	} else if !errors.Is(err, environments.ErrScanBlocked) {
		t.Fatalf("want ErrScanBlocked, got %v", err)
	}
}
