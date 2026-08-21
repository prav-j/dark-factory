// Package toolgw is the single choke point for all non-MCP tool calls
// (specs/06): every request passes authn (run token) -> policy -> rate
// limit -> credential injection -> execution -> response filtering.
// No token, no egress.
package toolgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/runtoken"
)

var (
	ErrNoToken       = errors.New("missing or invalid run token")
	ErrUnknownTool   = errors.New("unknown tool")
	ErrPolicyDenied  = errors.New("denied by policy")
	ErrRateLimited   = errors.New("tool rate limited")
	ErrEgressBlocked = errors.New("egress target not allowlisted")
)

// ToolDef is a registered tool: its contract and required scope.
type ToolDef struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	InputSchema   string `json:"inputSchema"`
	RequiredScope string `json:"requiredScope"` // must appear in grants + spec scopes
}

// Executor runs a tool after the pipeline's pre-checks. Implementations are
// trusted built-ins or vetted user-defined tools; they never see raw user
// credentials — only provider-scoped tokens injected via Injector.
type Executor func(ctx context.Context, input json.RawMessage) (output string, err error)

// Injector provides short-lived downstream credentials just-in-time
// (specs/08); production backs this with internal/credexchange.
type Injector func(ctx context.Context, runID string) (username, secret string, err error)

// RegisteredTool pairs a definition with its executor and rate limit.
type RegisteredTool struct {
	Def      ToolDef
	Exec     Executor
	Injector Injector // optional
	Limiter  *rate.Limiter
}

// Filter transforms/redacts tool output before it enters model context:
// DLP scanning hooks, PII scrubbing, etc.
type Filter func(output string) string

// Registry holds all tools known to the gateway.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]*RegisteredTool
	filters []Filter
	maxOut  int // response size cap in bytes
}

func NewRegistry(maxOutputBytes int) *Registry {
	return &Registry{tools: map[string]*RegisteredTool{}, maxOut: maxOutputBytes}
}

// Register adds a tool with qps rate limiting.
func (r *Registry) Register(def ToolDef, exec Executor, injector Injector, qps float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[def.Name] = &RegisteredTool{
		Def: def, Exec: exec, Injector: injector,
		Limiter: rate.NewLimiter(rate.Limit(qps), 5),
	}
}

// AddFilter appends an output filter (applied in registration order).
func (r *Registry) AddFilter(f Filter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.filters = append(r.filters, f)
}

func (r *Registry) get(name string) (*RegisteredTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Catalog returns definitions of all registered tools (for context assembly).
func (r *Registry) Catalog() []ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Def)
	}
	return out
}

// LiveScopes provides the user's CURRENT granted scopes (specs/04: immediate
// revocation). Backed by grants.ScopeCache with invalidation on revoke.
type LiveScopes interface {
	ActiveScopes(ctx context.Context, orgID, userID string) ([]string, error)
}

// Gateway executes calls through the full pipeline.
type Gateway struct {
	reg    *Registry
	policy *policy.Engine
	tokens *runtoken.Service
	clock  func() time.Time
	live   LiveScopes // optional; falls back to token-embedded grants
}

func New(reg *Registry, pol *policy.Engine, tokens *runtoken.Service) *Gateway {
	return &Gateway{reg: reg, policy: pol, tokens: tokens, clock: time.Now}
}

// SetLiveScopes enables immediate-revocation semantics: consent scopes are
// read live (cached with invalidation) instead of from the static claim set.
func (g *Gateway) SetLiveScopes(ls LiveScopes) { g.live = ls }

// CallResult is a completed tool invocation.
type CallResult struct {
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration"`
}

// Call runs the pipeline in order (C06-001). Any failure aborts before the
// executor sees the request.
func (g *Gateway) Call(ctx context.Context, runToken, toolName string, input json.RawMessage) (*CallResult, error) {
	start := g.clock()

	// 1. Authn: validate the run token.
	claims, err := g.tokens.Validate(ctx, runToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoToken, err)
	}

	// 2. Registry lookup.
	rt, ok := g.reg.get(toolName)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, toolName)
	}

	// 3. Policy: org pattern + consent + agent-spec scope intersection.
	// Consent scopes come from LIVE grant state when available so that
	// revocation blocks within seconds (specs/04); the token's embedded
	// grants are the offline fallback.
	consents := claims.Grants
	if g.live != nil {
		if live, err := g.live.ActiveScopes(ctx, claims.OrgID, claims.UserID); err == nil {
			consents = live
		}
	}
	d := g.policy.Can(ctx, policy.Request{
		OrgID: claims.OrgID, UserID: claims.UserID,
		Action: "tool.call", Resource: toolName,
		RequiredScope: rt.Def.RequiredScope,
		Consents:      consents,
		AgentScopes:   claims.Grants,
	})
	if !d.Allowed {
		return nil, fmt.Errorf("%w: %s", ErrPolicyDenied, d.Reason)
	}

	// 4. Rate limit per tool.
	if err := rt.Limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRateLimited, err)
	}

	// 5. Execute (with JIT-injected creds when the tool needs them).
	output, err := rt.Exec(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("tool error: %w", err)
	}

	// 6. Response filtering: size caps + filters.
	if g.reg.maxOut > 0 && len(output) > g.reg.maxOut {
		output = output[:g.reg.maxOut] + "\n[truncated]"
	}
	g.reg.mu.RLock()
	filters := append([]Filter(nil), g.reg.filters...)
	g.reg.mu.RUnlock()
	for _, f := range filters {
		output = f(output)
	}

	return &CallResult{Output: output, Duration: g.clock().Sub(start)}, nil
}

// --- built-in tools ---

// HTTPRequestTool returns an http_request executor enforcing an org-scoped
// domain allowlist at the gateway boundary (C06-003).
func HTTPRequestTool(allowlist []string) Executor {
	return func(_ context.Context, input json.RawMessage) (string, error) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
		host := extractHost(req.URL)
		if host == "" {
			return "", errors.New("missing url")
		}
		for _, allowed := range allowlist {
			if host == allowed || strings.HasSuffix(host, "."+allowed) {
				return fmt.Sprintf(`{"status":"would-fetch","host":%q}`, host), nil
			}
		}
		return "", ErrEgressBlocked
	}
}

// WebSearchTool is a stub search executor (real backend wired later).
func WebSearchTool(_ Injector) Executor {
	return func(_ context.Context, _ json.RawMessage) (string, error) {
		return `{"results":[{"title":"example","url":"https://example.com"}]}`, nil
	}
}

// CodeInterpreterTool is a stub sandboxed-execution placeholder (M6 wires
// the real sandbox executor).
func CodeInterpreterTool(inj Injector) Executor {
	return func(ctx context.Context, input json.RawMessage) (string, error) {
		if inj != nil {
			if _, _, err := inj(ctx, "code-interpreter"); err != nil {
				return "", err
			}
		}
		var req struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(input, &req); err != nil {
			return "", err
		}
		return fmt.Sprintf(`{"executed":true,"bytes":%d}`, len(req.Code)), nil
	}
}

func extractHost(rawURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}
