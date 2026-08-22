package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/prav-j/dark-factory/internal/modelgw"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/toolgw"
)

// InternalGateway exposes the execution-plane gateways to session pods over
// HTTP. Every request carries the pod's Run Token; identity and grants come
// from it — the sandbox never holds provider credentials.
type InternalGateway struct {
	Tokens *runtoken.Service
	Model  Completer
	Tools  *toolgw.Gateway
}

// Completer abstracts the model gateway router.
type Completer interface {
	Complete(ctx context.Context, req modelgw.CompletionRequest) (*modelgw.CompletionResponse, error)
}

// Register mounts internal routes (run-token authenticated per call).
func (g *InternalGateway) Register(mux *http.ServeMux) {
	mux.HandleFunc("/internal/llm/complete", g.auth(g.complete))
	mux.HandleFunc("/internal/tools/call", g.auth(g.callTool))
	mux.HandleFunc("/internal/tools/catalog", g.auth(g.catalog))
}

func (g *InternalGateway) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.Tokens.Validate(r.Context(), bearerToken(r)); err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid run token")
			return
		}
		next(w, r)
	}
}

func (g *InternalGateway) complete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID     string            `json:"runId"`
		Agent     string            `json:"agent"`
		UserID    string            `json:"userId"`
		OrgID     string            `json:"orgId"`
		Model     string            `json:"model"`
		Messages  []modelgw.Message `json:"messages"`
		MaxTokens int               `json:"maxTokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	resp, err := g.Model.Complete(r.Context(), modelgw.CompletionRequest{
		Model: req.Model, Messages: req.Messages, MaxTokens: req.MaxTokens,
		RunID: req.RunID, Agent: req.Agent, UserID: req.UserID, OrgID: req.OrgID,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (g *InternalGateway) callTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunToken string          `json:"runToken"`
		Tool     string          `json:"tool"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	res, err := g.Tools.Call(r.Context(), req.RunToken, req.Tool, req.Input)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// catalog returns registered tool definitions for context assembly.
func (g *InternalGateway) catalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": g.Tools.Catalog()})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && len(h) >= 7 && (h[:7] == "Bearer " || h[:7] == "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
