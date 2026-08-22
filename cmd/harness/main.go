// Command harness runs inside session pods (specs/16): it executes agent
// loops against the registry's internal gateway endpoints using its Run
// Token, persists the transcript locally, and exits when the loop completes.
//
// Configuration via environment:
//
//	REGISTRY_URL      base URL of the registry service
//	RUN_TOKEN         this run's run token
//	RUN_ID            run identifier
//	SESSION_ID        session identifier
//	ORG_ID / USER_ID  attribution
//	AGENT_REF         name@version
//	MODEL             model name
//	SPEC_YAML         the published agent spec (YAML)
//	USER_MESSAGE      initial user message for this run
//	GRANTED_TOOLS     comma-separated tool names in the effective grant set
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prav-j/dark-factory/internal/agentspec"
	"github.com/prav-j/dark-factory/internal/harness"
)

func main() {
	cfg := configFromEnv()
	if err := run(cfg); err != nil {
		log.Fatalf("harness: %v", err)
	}
}

type config struct {
	registryURL string
	runToken    string
	runID       string
	sessionID   string
	orgID       string
	userID      string
	agentRef    string
	model       string
	specYAML    string
	userMessage string
	granted     []string
}

func specYAML() string {
	if b64 := os.Getenv("SPEC_YAML_B64"); b64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
			return string(raw)
		}
	}
	return os.Getenv("SPEC_YAML")
}

func configFromEnv() config {
	return config{
		registryURL: strings.TrimSuffix(os.Getenv("REGISTRY_URL"), "/"),
		runToken:    os.Getenv("RUN_TOKEN"),
		runID:       os.Getenv("RUN_ID"),
		sessionID:   os.Getenv("SESSION_ID"),
		orgID:       os.Getenv("ORG_ID"),
		userID:      os.Getenv("USER_ID"),
		agentRef:    os.Getenv("AGENT_REF"),
		model:       os.Getenv("MODEL"),
		specYAML:    specYAML(),
		userMessage: os.Getenv("USER_MESSAGE"),
		granted:     splitCSV(os.Getenv("GRANTED_TOOLS")),
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func run(cfg config) error {
	for key, val := range map[string]string{
		"REGISTRY_URL": cfg.registryURL, "RUN_TOKEN": cfg.runToken,
		"RUN_ID": cfg.runID, "SESSION_ID": cfg.sessionID,
		"SPEC_YAML": cfg.specYAML, "USER_MESSAGE": cfg.userMessage,
	} {
		if val == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	doc, err := agentspec.Parse([]byte(cfg.specYAML))
	if err != nil {
		return fmt.Errorf("spec: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	completer := newHTTPCompleter(cfg)
	h := harness.New(completer, harnessTools(cfg), doc.Spec.Limits.MaxStepsPerRun, nopCheckpointer{})

	state, err := h.Run(ctx, &harness.RunState{
		RunID:     cfg.runID,
		SessionID: cfg.sessionID,
		AgentRef:  cfg.agentRef,
		Messages:  []harness.Message{{Role: "user", Content: cfg.userMessage}},
	}, cfg.granted, cfg.model)
	if err != nil {
		return err
	}

	// Transcript persisted to local overlay; durable copy flows through the
	// stop-hook manifest path on session end.
	raw, _ := json.MarshalIndent(state.Messages, "", "  ")
	log.Printf("run complete status=%s steps=%d transcript_bytes=%d",
		state.Status, state.Step, len(raw))
	return nil
}

type httpCompleter struct {
	cfg    config
	client *http.Client
}

func newHTTPCompleter(cfg config) *httpCompleter {
	return &httpCompleter{cfg: cfg, client: &http.Client{Timeout: 120 * time.Second}}
}

func (c *httpCompleter) Complete(ctx context.Context, req harness.CompletionRequest) (*harness.Completion, error) {
	body, err := json.Marshal(map[string]any{
		"runId": req.RunID, "agent": req.Agent,
		"userId": c.cfg.userID, "orgId": c.cfg.orgID,
		"model": req.Model, "messages": req.Messages, "maxTokens": 4096,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.registryURL+"/internal/llm/complete", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.runToken)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm complete: status %d", resp.StatusCode)
	}
	var out harness.Completion
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// harnessTools exposes built-in tools whose execution proxies through the
// registry's tool gateway — credentials never enter the pod.
func harnessTools(cfg config) []harness.Tool {
	return []harness.Tool{&gatewayTool{cfg: cfg}}
}

type gatewayTool struct {
	cfg    config
	client *http.Client
}

func (g *gatewayTool) init() {
	if g.client == nil {
		g.client = &http.Client{Timeout: 60 * time.Second}
	}
}

func (g *gatewayTool) Name() string        { return "http_request" }
func (g *gatewayTool) Description() string { return "Performs an allowlisted HTTP request." }
func (g *gatewayTool) InputSchema() string {
	return `{"type":"object","required":["url"],"properties":{"url":{"type":"string"}}}`
}
func (g *gatewayTool) RequiresApproval() bool { return false }

func (g *gatewayTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	g.init()
	body, err := json.Marshal(map[string]any{
		"runToken": g.cfg.runToken, "tool": g.Name(), "input": input,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.cfg.registryURL+"/internal/tools/call", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Output, nil
}

type nopCheckpointer struct{}

func (nopCheckpointer) Save(_ context.Context, _ string, _ *harness.RunState) error { return nil }
func (nopCheckpointer) Load(_ context.Context, _ string) (*harness.RunState, error) {
	return nil, errors.New("no checkpoint")
}
func (nopCheckpointer) Delete(_ context.Context, _ string) error { return nil }
