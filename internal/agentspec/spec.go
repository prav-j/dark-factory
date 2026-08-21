// Package agentspec defines the declarative agent spec (specs/03-data-model.md),
// parses YAML into typed structs with strict field checking, applies semantic
// validation, and produces canonical JSON for content hashing.
package agentspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the root agent specification.
type Spec struct {
	Model       Model        `json:"model" yaml:"model"`
	Prompt      Prompt       `json:"prompt" yaml:"prompt"`
	Tools       []Tool       `json:"tools,omitempty" yaml:"tools"`
	MCPServers  []MCPServer  `json:"mcpServers,omitempty" yaml:"mcpServers"`
	Environment *Environment `json:"environment,omitempty" yaml:"environment"`
	Memory      *Memory      `json:"memory,omitempty" yaml:"memory"`
	Triggers    []Trigger    `json:"triggers" yaml:"triggers"`
	Limits      Limits       `json:"limits" yaml:"limits"`
}

type Model struct {
	Provider string         `json:"provider" yaml:"provider"`
	Name     string         `json:"name" yaml:"name"`
	Params   map[string]any `json:"params,omitempty" yaml:"params"`
}

type Prompt struct {
	Type  string `json:"type" yaml:"type"` // inline | template-ref
	Value string `json:"value" yaml:"value"`
}

type Tool struct {
	Ref              string   `json:"ref" yaml:"ref"`
	Scopes           []string `json:"scopes,omitempty" yaml:"scopes"`
	RequiresApproval bool     `json:"requiresApproval,omitempty" yaml:"requiresApproval"`
}

type MCPServer struct {
	Ref          string   `json:"ref" yaml:"ref"`
	Version      string   `json:"version" yaml:"version"`
	Auth         string   `json:"auth,omitempty" yaml:"auth"` // oauth-user (default)
	AllowedTools []string `json:"allowedTools,omitempty" yaml:"allowedTools"`
}

type Environment struct {
	Image     Image       `json:"image" yaml:"image"`
	Setup     []SetupStep `json:"setup,omitempty" yaml:"setup"`
	Resources Resources   `json:"resources,omitempty" yaml:"resources"`
	Repos     []Repo      `json:"repos,omitempty" yaml:"repos"`
	Network   string      `json:"network,omitempty" yaml:"network"` // allowlist
}

type Image struct {
	Type       string `json:"type" yaml:"type"` // docker-ref | dockerfile
	Ref        string `json:"ref,omitempty" yaml:"ref"`
	Dockerfile string `json:"dockerfile,omitempty" yaml:"dockerfile"`
}

type SetupStep struct {
	Run string `json:"run" yaml:"run"`
}

type Resources struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu"`
	Memory string `json:"memory,omitempty" yaml:"memory"`
	Disk   string `json:"disk,omitempty" yaml:"disk"`
}

type Repo struct {
	URL         string `json:"url" yaml:"url"`
	Ref         string `json:"ref" yaml:"ref"`
	ClonePolicy string `json:"clonePolicy,omitempty" yaml:"clonePolicy"` // env-build (default) | session-start
	Path        string `json:"path" yaml:"path"`
	Auth        string `json:"auth,omitempty" yaml:"auth"`
}

type Memory struct {
	Type      string `json:"type" yaml:"type"` // conversation | vector-store
	Retention string `json:"retention,omitempty" yaml:"retention"`
}

type Trigger struct {
	Type string `json:"type" yaml:"type"` // chat | schedule | webhook
	Cron string `json:"cron,omitempty" yaml:"cron"`
}

type Limits struct {
	MaxStepsPerRun   int     `json:"maxStepsPerRun" yaml:"maxStepsPerRun"`
	MaxTokensPerRun  int     `json:"maxTokensPerRun" yaml:"maxTokensPerRun"`
	MonthlyBudgetUSD float64 `json:"monthlyBudgetUsd" yaml:"monthlyBudgetUsd"`
}

var (
	cronRe      = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+\S+\s+\S+$`)
	retentionRe = regexp.MustCompile(`^[0-9]+d$`)
)

// Document is the full agent spec file: envelope + inner Spec.
type Document struct {
	APIVersion string   `json:"apiVersion" yaml:"apiVersion"`
	Kind       string   `json:"kind" yaml:"kind"`
	Metadata   Metadata `json:"metadata" yaml:"metadata"`
	Spec       Spec     `json:"spec" yaml:"spec"`
}

type Metadata struct {
	Name  string `json:"name" yaml:"name"`
	Owner string `json:"owner" yaml:"owner"`
}

// Parse decodes strict YAML (unknown fields rejected) and validates.
func Parse(data []byte) (*Document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("decode spec: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("validate spec: %w", err)
	}
	return &d, nil
}

// Validate checks the envelope and the inner spec.
func (d *Document) Validate() error {
	if d.APIVersion != "agents/v1" {
		return fmt.Errorf("apiVersion must be agents/v1, got %q", d.APIVersion)
	}
	if d.Kind != "Agent" {
		return fmt.Errorf("kind must be Agent, got %q", d.Kind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if strings.TrimSpace(d.Metadata.Owner) == "" {
		return fmt.Errorf("metadata.owner is required")
	}
	return d.Spec.Validate()
}

// Validate enforces the semantic rules from specs/03 and specs/15.
func (s *Spec) Validate() error {
	if s.Model.Provider == "" {
		return fmt.Errorf("model.provider is required")
	}
	if s.Model.Name == "" {
		return fmt.Errorf("model.name is required")
	}

	switch s.Prompt.Type {
	case "inline":
		if strings.TrimSpace(s.Prompt.Value) == "" {
			return fmt.Errorf("prompt.value is required when type=inline")
		}
	case "template-ref":
		if strings.TrimSpace(s.Prompt.Value) == "" {
			return fmt.Errorf("prompt.value must hold the template ref when type=template-ref")
		}
	default:
		return fmt.Errorf("prompt.type must be inline or template-ref, got %q", s.Prompt.Type)
	}

	if err := validateTools(s.Tools); err != nil {
		return err
	}
	if err := validateMCPServers(s.MCPServers); err != nil {
		return err
	}
	if s.Environment != nil {
		if err := s.Environment.validate(); err != nil {
			return err
		}
	}
	if s.Memory != nil {
		switch s.Memory.Type {
		case "conversation", "vector-store":
		default:
			return fmt.Errorf("memory.type must be conversation or vector-store, got %q", s.Memory.Type)
		}
		if s.Memory.Retention != "" && !retentionRe.MatchString(s.Memory.Retention) {
			return fmt.Errorf("memory.retention must look like \"30d\", got %q", s.Memory.Retention)
		}
	}
	if err := validateTriggers(s.Triggers); err != nil {
		return err
	}
	if err := s.Limits.validate(); err != nil {
		return err
	}
	return nil
}

func validateTools(tools []Tool) error {
	seen := map[string]bool{}
	for i, t := range tools {
		if t.Ref == "" {
			return fmt.Errorf("tools[%d].ref is required", i)
		}
		if seen[t.Ref] {
			return fmt.Errorf("duplicate tool ref %q", t.Ref)
		}
		seen[t.Ref] = true
		for _, sc := range t.Scopes {
			if strings.TrimSpace(sc) == "" {
				return fmt.Errorf("tools[%d].scopes contains an empty scope", i)
			}
		}
	}
	return nil
}

func validateMCPServers(servers []MCPServer) error {
	seen := map[string]bool{}
	for i, m := range servers {
		if m.Ref == "" {
			return fmt.Errorf("mcpServers[%d].ref is required", i)
		}
		if seen[m.Ref] {
			return fmt.Errorf("duplicate mcpServer ref %q", m.Ref)
		}
		seen[m.Ref] = true
		if m.Version == "" {
			return fmt.Errorf("mcpServers[%d].version is required", i)
		}
		if m.Auth != "" && m.Auth != "oauth-user" {
			return fmt.Errorf("mcpServers[%d].auth must be oauth-user, got %q", i, m.Auth)
		}
	}
	return nil
}

func (e *Environment) validate() error {
	switch e.Image.Type {
	case "docker-ref":
		if e.Image.Ref == "" {
			return fmt.Errorf("environment.image.ref is required when type=docker-ref")
		}
	case "dockerfile":
		if strings.TrimSpace(e.Image.Dockerfile) == "" {
			return fmt.Errorf("environment.image.dockerfile is required when type=dockerfile")
		}
	default:
		return fmt.Errorf("environment.image.type must be docker-ref or dockerfile, got %q", e.Image.Type)
	}
	seenPaths := map[string]bool{}
	for i, r := range e.Repos {
		if !strings.HasPrefix(r.URL, "https://") {
			return fmt.Errorf("environment.repos[%d].url must be https, got %q", i, r.URL)
		}
		if r.Ref == "" {
			return fmt.Errorf("environment.repos[%d].ref is required", i)
		}
		cp := r.ClonePolicy
		if cp == "" {
			cp = "env-build" // default per specs/15
		}
		if cp != "env-build" && cp != "session-start" {
			return fmt.Errorf("environment.repos[%d].clonePolicy must be env-build or session-start, got %q", i, cp)
		}
		if !strings.HasPrefix(r.Path, "/") {
			return fmt.Errorf("environment.repos[%d].path must be absolute, got %q", i, r.Path)
		}
		if seenPaths[r.Path] {
			return fmt.Errorf("duplicate environment.repos[%d].path %q", i, r.Path)
		}
		seenPaths[r.Path] = true
	}
	return nil
}

func validateTriggers(triggers []Trigger) error {
	if len(triggers) == 0 {
		return fmt.Errorf("at least one trigger is required")
	}
	hasSchedule := false
	for i, tr := range triggers {
		switch tr.Type {
		case "chat", "webhook":
		case "schedule":
			hasSchedule = true
			if !cronRe.MatchString(strings.TrimSpace(tr.Cron)) {
				return fmt.Errorf("triggers[%d]: schedule trigger requires a 5-field cron expression, got %q", i, tr.Cron)
			}
		default:
			return fmt.Errorf("triggers[%d].type must be chat, schedule, or webhook, got %q", i, tr.Type)
		}
	}
	_ = hasSchedule
	return nil
}

func (l Limits) validate() error {
	if l.MaxStepsPerRun <= 0 {
		return fmt.Errorf("limits.maxStepsPerRun must be > 0")
	}
	if l.MaxTokensPerRun <= 0 {
		return fmt.Errorf("limits.maxTokensPerRun must be > 0")
	}
	if l.MonthlyBudgetUSD <= 0 {
		return fmt.Errorf("limits.monthlyBudgetUsd must be > 0")
	}
	return nil
}

// CanonicalJSON returns deterministic JSON of the inner spec for content
// hashing: field order is fixed by declaration, map keys sorted by encoding/json.
func (d *Document) CanonicalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d.Spec); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}
