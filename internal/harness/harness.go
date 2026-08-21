// Package harness implements the agent loop runtime (specs/05): a
// ReAct/tool-calling loop with maxSteps enforcement, tool filtering to the
// effective grant set, per-step checkpointing, and human-in-the-loop
// approval gates for sensitive tools.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrMaxSteps     = errors.New("max steps exceeded")
	ErrUnknownTool  = errors.New("tool not granted or unknown")
	ErrNoCheckpoint = errors.New("no checkpoint for run")
	ErrNotPending   = errors.New("run is not awaiting approval")
)

// Tool is a callable capability exposed to the model.
type Tool interface {
	Name() string
	Description() string
	InputSchema() string // JSON schema string
	RequiresApproval() bool
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

// Message is one conversation turn in the loop.
type Message struct {
	Role    string `json:"role"` // user | assistant | tool
	Content string `json:"content"`
}

// Completer abstracts the model gateway.
type Completer interface {
	Complete(ctx context.Context, req CompletionRequest) (*Completion, error)
}

// CompletionRequest carries the visible conversation + tool specs.
type CompletionRequest struct {
	Model    string     `json:"model"`
	Messages []Message  `json:"messages"`
	Tools    []ToolSpec `json:"tools"`
	RunID    string     `json:"runId"`
	Agent    string     `json:"agent"`
}

type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema string `json:"input_schema"`
}

type Completion struct {
	Text        string
	StopReason  string // "end_turn" | "tool_use"
	ToolName    string
	ToolInput   string
	InputTokens int
	OutputTok   int
}

// Checkpointer persists loop state after each step so runs survive infra
// failures (DDB/object-store implementation lands with #24; memory here).
type Checkpointer interface {
	Save(ctx context.Context, runID string, state *RunState) error
	Load(ctx context.Context, runID string) (*RunState, error)
	Delete(ctx context.Context, runID string) error
}

// RunState is the checkpointed loop state.
type RunState struct {
	RunID        string    `json:"runId"`
	SessionID    string    `json:"sessionId"`
	AgentRef     string    `json:"agentRef"`
	Messages     []Message `json:"messages"`
	Step         int       `json:"step"`
	Status       string    `json:"status"` // running | awaiting_approval | done | denied
	PendingTool  string    `json:"pendingTool,omitempty"`
	PendingInput string    `json:"pendingInput,omitempty"`
	FinalText    string    `json:"finalText,omitempty"`
	GrantedTools []string  `json:"grantedTools"`
}

// Harness executes agent loops.
type Harness struct {
	completer   Completer
	tools       map[string]Tool
	maxSteps    int
	checkpoints Checkpointer
}

func New(completer Completer, tools []Tool, maxSteps int, checkpoints Checkpointer) *Harness {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return &Harness{completer: completer, tools: m, maxSteps: maxSteps, checkpoints: checkpoints}
}

// Run drives the loop from a user message. Only tools named in grantedTools
// are ever exposed to the model (specs/05 C05-001).
func (h *Harness) Run(ctx context.Context, state *RunState, grantedTools []string, model string) (*RunState, error) {
	state.GrantedTools = grantedTools
	state.Status = "running"
	for {
		if state.Step >= h.maxSteps {
			state.Status = "done"
			state.FinalText = ""
			_ = h.checkpoints.Save(ctx, state.RunID, state)
			return state, fmt.Errorf("%w after %d steps", ErrMaxSteps, h.maxSteps)
		}

		resp, err := h.completer.Complete(ctx, h.buildRequest(state, grantedTools, model))
		if err != nil {
			return state, err
		}
		state.Step++
		state.Messages = append(state.Messages, Message{Role: "assistant", Content: resp.Text})

		switch resp.StopReason {
		case "end_turn":
			state.Status = "done"
			state.FinalText = resp.Text
			_ = h.checkpoints.Save(ctx, state.RunID, state)
			return state, nil

		case "tool_use":
			tool, ok := h.tools[resp.ToolName]
			if !ok || !granted(grantedTools, resp.ToolName) {
				state.Status = "done"
				state.FinalText = ""
				_ = h.checkpoints.Save(ctx, state.RunID, state)
				return state, fmt.Errorf("%w: %q", ErrUnknownTool, resp.ToolName)
			}
			if tool.RequiresApproval() {
				state.Status = "awaiting_approval"
				state.PendingTool = resp.ToolName
				state.PendingInput = resp.ToolInput
				_ = h.checkpoints.Save(ctx, state.RunID, state)
				return state, nil // caller surfaces the approval request
			}
			if err := h.executeAndAppend(ctx, state, tool, resp.ToolInput); err != nil {
				return state, err
			}
			_ = h.checkpoints.Save(ctx, state.RunID, state)

		default:
			state.Status = "done"
			state.FinalText = resp.Text
			_ = h.checkpoints.Save(ctx, state.RunID, state)
			return state, nil
		}
	}
}

// Resume continues an awaiting_approval run after the user's decision.
func (h *Harness) Resume(ctx context.Context, runID string, approved bool, model string) (*RunState, error) {
	state, err := h.checkpoints.Load(ctx, runID)
	if err != nil {
		return nil, ErrNoCheckpoint
	}
	if state.Status != "awaiting_approval" {
		return nil, ErrNotPending
	}

	tool := h.tools[state.PendingTool]
	if !approved || tool == nil {
		state.Status = "done"
		state.FinalText = "user declined the tool call; stopping."
		state.PendingTool, state.PendingInput = "", ""
		_ = h.checkpoints.Save(ctx, state.RunID, state)
		return state, nil
	}
	state.Messages = append(state.Messages,
		Message{Role: "assistant", Content: fmt.Sprintf("[approved tool %s]", state.PendingTool)})
	if err := h.executeAndAppend(ctx, state, tool, state.PendingInput); err != nil {
		return nil, err
	}
	state.PendingTool, state.PendingInput = "", ""
	_ = h.checkpoints.Save(ctx, state.RunID, state)
	return h.Run(ctx, state, h.grantedFromState(state), model)
}

// grantedFromState restores the grant list saved at run start.
func (h *Harness) grantedFromState(state *RunState) []string {
	return state.GrantedTools
}

func (h *Harness) executeAndAppend(ctx context.Context, state *RunState, tool Tool, input string) error {
	out, err := tool.Execute(ctx, json.RawMessage(input))
	if err != nil {
		out = fmt.Sprintf("tool error: %v", err) // feed failures back to the model
	}
	state.Messages = append(state.Messages, Message{
		Role: "tool", Content: fmt.Sprintf("[%s] %s", tool.Name(), out),
	})
	return nil
}

func (h *Harness) buildRequest(state *RunState, grantedTools []string, model string) CompletionRequest {
	specs := make([]ToolSpec, 0, len(grantedTools))
	for _, name := range grantedTools {
		if t, ok := h.tools[name]; ok {
			specs = append(specs, ToolSpec{Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema()})
		}
	}
	return CompletionRequest{
		Model: model, Messages: state.Messages, Tools: specs,
		RunID: state.RunID, Agent: state.AgentRef,
	}
}

func granted(list []string, name string) bool {
	for _, g := range list {
		if g == name {
			return true
		}
	}
	return false
}
