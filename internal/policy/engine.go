// Package policy implements the Permission Broker's decision engine
// (specs/04-identity-scoping.md): deny-by-default, org policies evaluated by
// an embedded OPA/Rego engine, and effective scope = intersection of org
// policy, user consents, and agent spec scopes.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/open-policy-agent/opa/rego"
)

const module = `package authz

default allow = false

allow {
	org_allows
}

# patterns are "action:resource" globs, e.g. "tool.call:builtin/*"
org_allows {
	pattern := data.authz_data.org_patterns[_]
	glob.match(pattern, [":"], sprintf("%v:%v", [input.action, input.resource]))
}
`

// Request is one capability check.
type Request struct {
	OrgID         string
	UserID        string
	Action        string // e.g. "tool.call", "mcp.call"
	Resource      string // e.g. "builtin/web_search", "github-official"
	RequiredScope string // empty => org policy alone decides
	Consents      []string
	AgentScopes   []string
}

// Decision records the outcome plus which layer decided it (audit trail).
type Decision struct {
	Allowed bool
	Reason  string
}

func (d Decision) String() string { return d.Reason }

// Engine evaluates capability checks. Safe for concurrent use.
type Engine struct {
	mu       sync.RWMutex
	query    rego.PreparedEvalQuery
	patterns []string
}

// NewEngine compiles the policy module with no org patterns (deny-all).
func NewEngine() (*Engine, error) {
	e := &Engine{}
	if err := e.SetOrgPatterns(nil); err != nil {
		return nil, err
	}
	return e, nil
}

// SetOrgPatterns replaces the org's allowed action:resource glob patterns and
// recompiles the prepared query. Patterns are exact per-org in v1; per-org
// data documents arrive with multi-tenant policy storage.
func (e *Engine) SetOrgPatterns(patterns []string) error {
	clean := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			clean = append(clean, p)
		}
	}

	dataModule := fmt.Sprintf("package authz_data\n\norg_patterns := %s", mustJSON(clean))
	q, err := rego.New(
		rego.Query("data.authz.allow"),
		rego.Module("authz.rego", module),
		rego.Module("authz_data.rego", dataModule),
	).PrepareForEval(context.Background())
	if err != nil {
		return fmt.Errorf("compile policies: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.query = q
	e.patterns = clean
	return nil
}

// Can evaluates the request. Deny-by-default: every layer must pass.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (e *Engine) Can(ctx context.Context, req Request) Decision {
	e.mu.RLock()
	query := e.query
	e.mu.RUnlock()

	input := map[string]any{
		"org":      req.OrgID,
		"user":     req.UserID,
		"action":   req.Action,
		"resource": req.Resource,
	}

	rs, err := query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return Decision{false, fmt.Sprintf("policy evaluation error: %v", err)}
	}
	if len(rs) == 0 || rs[0].Expressions[0].Value != true {
		return Decision{false, fmt.Sprintf("denied by org policy for %s:%s", req.Action, req.Resource)}
	}

	if req.RequiredScope == "" {
		return Decision{true, "allowed by org policy"}
	}

	inSet := func(set []string) bool {
		for _, s := range set {
			if s == req.RequiredScope {
				return true
			}
		}
		return false
	}
	if !inSet(req.Consents) {
		return Decision{false, fmt.Sprintf("missing user consent for scope %q", req.RequiredScope)}
	}
	if !inSet(req.AgentScopes) {
		return Decision{false, fmt.Sprintf("scope %q not declared in agent spec", req.RequiredScope)}
	}
	return Decision{true, "allowed: org policy + consent + agent spec all grant scope"}
}
