package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/prav-j/dark-factory/internal/harness"
)

// --- test fakes ---

type scriptedCompleter struct {
	script []harness.Completion
	calls  int
	seen   [][]harness.ToolSpec
}

func (s *scriptedCompleter) Complete(_ context.Context, req harness.CompletionRequest) (*harness.Completion, error) {
	idx := s.calls
	if idx >= len(s.script) {
		return nil, errors.New("script exhausted")
	}
	s.calls++
	s.seen = append(s.seen, req.Tools)
	return &s.script[idx], nil
}

type echoTool struct {
	name       string
	approval   bool
	lastInput  string
	executions int
}

func (t *echoTool) Name() string                { return t.name }
func (t *echoTool) Description() string         { return "echoes input" }
func (t *echoTool) InputSchema() string         { return `{"type":"object"}` }
func (t *echoTool) RequiresApproval() bool      { return t.approval }
func (t *echoTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	t.executions++
	t.lastInput = string(in)
	return "echo:" + string(in), nil
}

type memCheckpoints struct{ states map[string]*harness.RunState }

func newCheckpointer() *memCheckpoints { return &memCheckpoints{states: map[string]*harness.RunState{}} }

func (m *memCheckpoints) Save(_ context.Context, id string, s *harness.RunState) error {
	cp := *s
	m.states[id] = &cp
	return nil
}
func (m *memCheckpoints) Load(_ context.Context, id string) (*harness.RunState, error) {
	s, ok := m.states[id]
	if !ok {
		return nil, errors.New("missing")
	}
	return s, nil
}
func (m *memCheckpoints) Delete(_ context.Context, id string) error {
	delete(m.states, id)
	return nil
}

// --- tests ---

// C05-001: multi-step tool loop where only granted tools are exposed.
func TestMultiStepToolLoopFiltersTools(t *testing.T) {
	search := &echoTool{name: "search"}
	danger := &echoTool{name: "deploy_prod", approval: true} // NOT granted
	completer := &scriptedCompleter{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "search", ToolInput: `{"q":"specs"}`},
		{StopReason: "end_turn", Text: "found it"},
	}}
	h := harness.New(completer, []harness.Tool{search, danger}, 10, newCheckpointer())

	state, err := h.Run(context.Background(), &harness.RunState{
		RunID: "run-1", AgentRef: "bot@v1",
		Messages: []harness.Message{{Role: "user", Content: "find specs"}},
	}, []string{"search"}, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if state.FinalText != "found it" || search.executions != 1 {
		t.Fatalf("state=%+v executions=%d", state, search.executions)
	}

	// The model must never see the ungranted deploy_prod tool.
	for i, tools := range completer.seen {
		for _, ts := range tools {
			if ts.Name == "deploy_prod" {
				t.Fatalf("ungranted tool exposed to model at call %d", i)
			}
		}
	}
	if len(completer.seen[0]) != 1 {
		t.Fatalf("granted tool specs = %+v, want only [search]", completer.seen[0])
	}
}

// Tool failures feed back into the loop instead of crashing it.
func TestToolErrorFedBackToModel(t *testing.T) {
	failing := errTool{}
	completer := &scriptedCompleter{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "boom", ToolInput: `{}`},
		{StopReason: "end_turn", Text: "handled the failure"},
	}}
	h := harness.New(completer, []harness.Tool{failing}, 5, newCheckpointer())
	state, err := h.Run(context.Background(), &harness.RunState{
		RunID: "run-e", Messages: []harness.Message{{Role: "user", Content: "go"}},
	}, []string{"boom"}, "m")
	if err != nil {
		t.Fatal(err)
	}
	var lastToolMsg string
	for _, m := range state.Messages {
		if m.Role == "tool" {
			lastToolMsg = m.Content
		}
	}
	if !strings.Contains(lastToolMsg, "tool error") {
		t.Fatalf("tool failure not fed back: %q", lastToolMsg)
	}
}

type errTool struct{}

func (errTool) Name() string           { return "boom" }
func (errTool) Description() string    { return "" }
func (errTool) InputSchema() string    { return "{}" }
func (errTool) RequiresApproval() bool { return false }
func (errTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("exploded")
}

// Unknown/ungranted tool invocation aborts the run.
func TestUngrantedToolInvocationAborts(t *testing.T) {
	search := &echoTool{name: "search"}
	completer := &scriptedCompleter{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "deploy_prod", ToolInput: `{}`},
	}}
	h := harness.New(completer, []harness.Tool{search}, 5, newCheckpointer())
	_, err := h.Run(context.Background(), &harness.RunState{
		RunID: "run-x", Messages: []harness.Message{{Role: "user", Content: "go"}},
	}, []string{"search"}, "m")
	if !errors.Is(err, harness.ErrUnknownTool) {
		t.Fatalf("err = %v, want ErrUnknownTool", err)
	}
}

// C05-002 / C16-003 groundwork: HITL pause -> approve -> resume completes;
// deny path ends the run without executing the tool.
func TestApprovalRoundTrip(t *testing.T) {
	deploy := &echoTool{name: "deploy_prod", approval: true}
	completer := &scriptedCompleter{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "deploy_prod", ToolInput: `{"env":"prod"}`},
		{StopReason: "end_turn", Text: "deployed"},
	}}
	h := harness.New(completer, []harness.Tool{deploy}, 10, newCheckpointer())

	// Pause on approval.
	state, err := h.Run(context.Background(), &harness.RunState{
		RunID: "run-a", Messages: []harness.Message{{Role: "user", Content: "ship it"}},
	}, []string{"deploy_prod"}, "m")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "awaiting_approval" || state.PendingTool != "deploy_prod" {
		t.Fatalf("state = %+v, want awaiting_approval", state)
	}
	if deploy.executions != 0 {
		t.Fatal("tool must not execute before approval")
	}

	// Deny first: run ends, tool still not executed.
	denied, err := h.Resume(context.Background(), "run-a", false, "m")
	if err != nil {
		t.Fatal(err)
	}
	if denied.Status != "done" || deploy.executions != 0 {
		t.Fatalf("deny path wrong: %+v executions=%d", denied, deploy.executions)
	}
}

func TestApprovalApproveResumesToCompletion(t *testing.T) {
	deploy := &echoTool{name: "deploy_prod", approval: true}
	h := harness.New(
		&scriptedCompleter{script: []harness.Completion{
			{StopReason: "tool_use", ToolName: "deploy_prod", ToolInput: `{"env":"prod"}`},
			{StopReason: "end_turn", Text: "deployed"},
		}},
		[]harness.Tool{deploy}, 10, newCheckpointer(),
	)

	state, _ := h.Run(context.Background(), &harness.RunState{
		RunID: "run-b", Messages: []harness.Message{{Role: "user", Content: "ship it"}},
	}, []string{"deploy_prod"}, "m")
	if state.Status != "awaiting_approval" {
		t.Fatalf("want awaiting_approval, got %s", state.Status)
	}

	final, err := h.Resume(context.Background(), "run-b", true, "m")
	if err != nil {
		t.Fatal(err)
	}
	if final.FinalText != "deployed" || deploy.executions != 1 || deploy.lastInput != `{"env":"prod"}` {
		t.Fatalf("final=%+v executions=%d input=%q", final, deploy.executions, deploy.lastInput)
	}
}

func TestMaxStepsEnforced(t *testing.T) {
	// Infinite tool-use script via repeating completions is bounded by
	// scripted length; simulate by maxSteps=1 with a tool_use first step.
	loop := &echoTool{name: "loop"}
	h := harness.New(&scriptedCompleter{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "loop", ToolInput: `{}`},
	}}, []harness.Tool{loop}, 1, newCheckpointer())

	_, err := h.Run(context.Background(), &harness.RunState{
		RunID: "run-m", Messages: []harness.Message{{Role: "user", Content: "go"}},
	}, []string{"loop"}, "m")
	if !errors.Is(err, harness.ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
}

func TestCheckpointAfterEachStep(t *testing.T) {
	cp := newCheckpointer()
	search := &echoTool{name: "search"}
	h := harness.New(&scriptedCompleter{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "search", ToolInput: `{}`},
		{StopReason: "end_turn", Text: "done"},
	}}, []harness.Tool{search}, 10, cp)

	_, err := h.Run(context.Background(), &harness.RunState{
		RunID: "run-c", Messages: []harness.Message{{Role: "user", Content: "go"}},
	}, []string{"search"}, "m")
	if err != nil {
		t.Fatal(err)
	}
	saved := cp.states["run-c"]
	if saved == nil {
		t.Fatal("no final checkpoint")
	}
	if saved.Step != 2 || len(saved.Messages) < 4 {
		t.Fatalf("checkpoint state incomplete: %+v", saved)
	}
}
