package environments

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// TrivyScanner implements Scanner by shelling out to the trivy CLI
// (production backend). RefFor maps a content digest to a scannable image
// reference in the platform registry.
type TrivyScanner struct {
	Binary string                     // default "trivy"
	RefFor func(digest string) string // digest -> registry image ref
}

func (s *TrivyScanner) binary() string {
	if s.Binary == "" {
		return "trivy"
	}
	return s.Binary
}

type trivyReport struct {
	Results []struct {
		Vulnerabilities []struct {
			ID       string `json:"VulnerabilityID"`
			Severity string `json:"Severity"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// Scan runs trivy and flattens findings.
func (s *TrivyScanner) Scan(ctx context.Context, digest string) ([]Vulnerability, error) {
	ref := "unknown"
	if s.RefFor != nil {
		ref = s.RefFor(digest)
	}
	out, err := exec.CommandContext(ctx, s.binary(),
		"image", "--format", "json", "--scanners", "vuln", "--quiet", ref,
	).Output()
	if err != nil {
		// trivy exits non-zero on found vulns only with --exit-code; without
		// it a real failure here is an execution error worth surfacing.
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, fmt.Errorf("trivy: %w", err)
		}
	}
	var report trivyReport
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, fmt.Errorf("trivy output: %w", err)
	}
	var vulns []Vulnerability
	for _, r := range report.Results {
		for _, v := range r.Vulnerabilities {
			vulns = append(vulns, Vulnerability{ID: v.ID, Severity: v.Severity})
		}
	}
	return vulns, nil
}
