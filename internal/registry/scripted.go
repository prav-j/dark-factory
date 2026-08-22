package registry

import (
	"context"
	"encoding/json"
	"os"

	"github.com/prav-j/dark-factory/internal/modelgw"
)

// ScriptedCompleter is a deterministic model provider for local runs: it
// replays responses (from SCRIPTED_RESPONSES JSON file when configured),
// repeating the last entry once exhausted. Production swaps this for an
// Anthropic HTTP provider behind the same Completer interface.
type ScriptedCompleter struct {
	responses []modelgw.CompletionResponse
	calls     int
}

// NewScriptedCompleter builds a completer from explicit responses.
func NewScriptedCompleter(responses []modelgw.CompletionResponse) *ScriptedCompleter {
	if len(responses) == 0 {
		responses = []modelgw.CompletionResponse{{
			Content:    "Acknowledged. No scripted responses configured.",
			StopReason: "end_turn",
		}}
	}
	return &ScriptedCompleter{responses: responses}
}

type scriptedFileEntry struct {
	Content    string `json:"content"`
	StopReason string `json:"stop_reason"`
}

// NewScriptedCompleterFromEnv loads scripted responses from the
// SCRIPTED_RESPONSES file.
func NewScriptedCompleterFromEnv() *ScriptedCompleter {
	var entries []scriptedFileEntry
	if data, err := os.ReadFile(os.Getenv("SCRIPTED_RESPONSES")); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	responses := make([]modelgw.CompletionResponse, len(entries))
	for i, e := range entries {
		responses[i] = modelgw.CompletionResponse{
			Content: e.Content, StopReason: e.StopReason,
		}
	}
	return NewScriptedCompleter(responses)
}

func (s *ScriptedCompleter) Complete(_ context.Context, _ modelgw.CompletionRequest) (*modelgw.CompletionResponse, error) {
	i := s.calls
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	s.calls++
	return &s.responses[i], nil
}

var _ Completer = (*ScriptedCompleter)(nil)
