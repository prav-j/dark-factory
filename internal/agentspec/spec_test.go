package agentspec_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/prav-j/dark-factory/internal/agentspec"
)

func TestParseGoldenRepoTriageBot(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden/repo-triage-bot.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	doc, err := agentspec.Parse(raw)
	if err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if doc.Metadata.Name != "repo-triage-bot" {
		t.Fatalf("name = %q", doc.Metadata.Name)
	}
	if len(doc.Spec.Tools) != 2 || len(doc.Spec.MCPServers) != 1 {
		t.Fatalf("tools/mcpServers not parsed: %+v", doc.Spec)
	}
	if doc.Spec.Limits.MaxStepsPerRun != 25 {
		t.Fatalf("limits not parsed: %+v", doc.Spec.Limits)
	}

	canon, err := doc.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	sum := sha256.Sum256(canon)
	const wantPrefix = "" // stability asserted below, value recorded for reuse
	_ = wantPrefix
	h1 := hex.EncodeToString(sum[:])

	// Re-parse and re-hash: canonical form must be stable.
	doc2, err := agentspec.Parse(raw)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	canon2, _ := doc2.CanonicalJSON()
	sum2 := sha256.Sum256(canon2)
	h2 := hex.EncodeToString(sum2[:])
	if h1 != h2 {
		t.Fatalf("canonical hash unstable:\n%s\n%s", h1, h2)
	}
	t.Logf("spec hash: %s", h1)
}

func TestParseRejectsUnknownFields(t *testing.T) {
	yaml := `
apiVersion: agents/v1
kind: Agent
metadata:
  name: x
  owner: user-1
spec:
  model:
    provider: anthropic
    name: m
  prompt:
    type: inline
    value: hi
  triggers:
    - type: chat
  limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}
  typoField: oops
`
	if _, err := agentspec.Parse([]byte(yaml)); err == nil {
		t.Fatal("unknown field must be rejected")
	} else if !strings.Contains(err.Error(), "typoField") {
		t.Fatalf("error should name the unknown field, got: %v", err)
	}
}

func TestValidateNegativeCases(t *testing.T) {
	base := func(body string) []byte {
		return []byte(`
apiVersion: agents/v1
kind: Agent
metadata:
  name: x
  owner: user-1
spec:
` + body)
	}
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing provider", `
    model: {name: m}
    prompt: {type: inline, value: hi}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"model.provider"},
		{"bad prompt type", `
    model: {provider: anthropic, name: m}
    prompt: {type: voice, value: hi}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"prompt.type"},
		{"inline without value", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: " "}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"prompt.value"},
		{"duplicate tool refs", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    tools:
      - {ref: builtin/web_search}
      - {ref: builtin/web_search}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"duplicate tool ref"},
		{"mcp missing version", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    mcpServers: [{ref: registry/github-official}]
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"version is required"},
		{"schedule without cron", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    triggers: [{type: schedule}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"cron"},
		{"no triggers", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    triggers: []
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"at least one trigger"},
		{"zero budget", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 0}`,
			"monthlyBudgetUsd"},
		{"repo clone policy invalid", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    environment:
      image: {type: docker-ref, ref: docker.io/acme/dev:1}
      repos:
        - {url: "https://github.com/acme/api", ref: main, clonePolicy: teleport, path: /workspace/api}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"clonePolicy"},
		{"repo path relative", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    environment:
      image: {type: docker-ref, ref: docker.io/acme/dev:1}
      repos:
        - {url: "https://github.com/acme/api", ref: main, path: workspace/api}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"path must be absolute"},
		{"dockerfile type without content", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    environment:
      image: {type: dockerfile}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"dockerfile is required"},
		{"http repo url", `
    model: {provider: anthropic, name: m}
    prompt: {type: inline, value: hi}
    environment:
      image: {type: docker-ref, ref: docker.io/acme/dev:1}
      repos:
        - {url: "http://github.com/acme/api", ref: main, path: /workspace/api}
    triggers: [{type: chat}]
    limits: {maxStepsPerRun: 5, maxTokensPerRun: 1000, monthlyBudgetUsd: 10}`,
			"must be https"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentspec.Parse(base(tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
