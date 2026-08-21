package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// LLMRequest is the normalized prompt record captured by FakeLLM.
type LLMRequest struct {
	Model    string          `json:"model"`
	Messages []LLMMessage    `json:"messages"`
	Tools    json.RawMessage `json:"tools,omitempty"`
}

// LLMMessage is a single conversation turn.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMResponse is one scripted reply. StopReason is "end_turn" or "tool_use".
type LLMResponse struct {
	Content    string `json:"content"`
	StopReason string `json:"stop_reason"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"`
}

// FakeLLM is an in-process scripted LLM server. Tests queue responses; each
// request pops the next one and is recorded for assertions. Once the script
// is exhausted, the last response repeats (useful for long loops).
type FakeLLM struct {
	srv *httptest.Server
	url string

	mu       sync.Mutex
	script   []LLMResponse
	requests []LLMRequest
}

// NewFakeLLM starts the fake provider and registers cleanup. An empty script
// defaults to a single "ok" end_turn response.
func NewFakeLLM(t *testing.T, script ...LLMResponse) *FakeLLM {
	t.Helper()
	f := &FakeLLM{script: script}
	if len(f.script) == 0 {
		f.script = []LLMResponse{{Content: "ok", StopReason: "end_turn"}}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", f.handle)
	f.srv = httptest.NewServer(mux)
	f.url = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base address model clients should point at.
func (f *FakeLLM) URL() string { return f.url }

// Requests returns all captured requests in order.
func (f *FakeLLM) Requests() []LLMRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]LLMRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *FakeLLM) handle(w http.ResponseWriter, r *http.Request) {
	var req LLMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	n := len(f.requests)
	resp := f.script[len(f.script)-1]
	if n < len(f.script) {
		resp = f.script[n]
	}
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
