// Package mcpproxy makes MCP first-class (specs/07): per-user routing so two
// users on the same server never share sessions or caches, tool filtering to
// the intersection of allowedTools patterns and granted scopes, and
// collision-safe namespacing (mcp__<server>__<tool>).
package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prav-j/dark-factory/internal/policy"
)

var (
	ErrNoConnection   = errors.New("no MCP connection for user/server")
	ErrUnknownTool    = errors.New("tool not exposed for this run")
	ErrPolicyDenied   = errors.New("denied by policy")
	ErrBadResponse    = errors.New("invalid tool response")
)

// ToolInfo describes a tool offered by an MCP server.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema string `json:"inputSchema,omitempty"`
}

// Session is one live connection to an MCP server on behalf of ONE user.
// Transports (streamable HTTP, managed stdio sidecars) implement this.
type Session interface {
	ListTools(ctx context.Context) ([]ToolInfo, error)
	CallTool(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, error)
	Close() error
}

// Dialer creates authenticated sessions using the user's stored credentials
// (resolved just-in-time — specs/08). One dialer call => one fresh session.
type Dialer interface {
	Dial(ctx context.Context, orgID, userID, serverRef string) (Session, error)
}

// Manager owns per-(user,server) connections and applies policy.
type Manager struct {
	mu      sync.Mutex
	conns   map[connKey]Session
	dialer  Dialer
	policy  *policy.Engine
	timeout time.Duration
	maxResp int
}

type connKey struct {
	org, user, server string
}

func NewManager(dialer Dialer, pol *policy.Engine, timeout time.Duration, maxResponseBytes int) *Manager {
	return &Manager{
		conns:   map[connKey]Session{},
		dialer:  dialer,
		policy:  pol,
		timeout: timeout,
		maxResp: maxResponseBytes,
	}
}

// CloseAll drops every cached connection (called at session end).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.conns {
		_ = s.Close()
		delete(m.conns, k)
	}
}

func (m *Manager) sessionFor(ctx context.Context, org, user, server string) (Session, error) {
	key := connKey{org, user, server}
	m.mu.Lock()
	if s, ok := m.conns[key]; ok {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	s, err := m.dialer.Dial(ctx, org, user, server) // dial outside lock
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.conns[key]; ok { // raced; prefer existing
		_ = s.Close()
		return existing, nil
	}
	m.conns[key] = s
	return s, nil
}

// ExposedTools returns the namespaced tool list visible to one run: the
// user's own connection's tools intersected with allowedTools globs, gated
// by policy on required scopes.
func (m *Manager) ExposedTools(ctx context.Context, req Authz, server string, allowedGlobs []string) ([]ToolInfo, error) {
	sess, err := m.sessionFor(ctx, req.OrgID, req.UserID, server)
	if err != nil {
		return nil, err
	}
	tools, err := sess.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	var out []ToolInfo
	for _, t := range tools {
		if !matchAny(allowedGlobs, t.Name) {
			continue
		}
		scope := fmt.Sprintf("mcp:%s:%s", server, t.Name)
		d := m.policy.Can(ctx, policy.Request{
			OrgID: req.OrgID, UserID: req.UserID,
			Action: "mcp.call", Resource: server,
			RequiredScope: scope, Consents: req.Grants, AgentScopes: req.Grants,
		})
		if d.Allowed {
			out = append(out, ToolInfo{
				Name:        Namespace(server, t.Name),
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	return out, nil
}

// Call executes a namespaced tool on behalf of one run, enforcing the same
// visibility rules as ExposedTools before any RPC leaves the gateway.
func (m *Manager) Call(ctx context.Context, req Authz, server, namespacedTool string, args json.RawMessage) (json.RawMessage, error) {
	server2, bare, ok := SplitNamespace(namespacedTool)
	if !ok || server2 != server {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, namespacedTool)
	}

	// Visibility re-check: the model may only call what it was shown.
	exposed, err := m.ExposedTools(ctx, req, server, []string{"*"})
	if err != nil {
		return nil, err
	}
	visible := false
	for _, t := range exposed {
		if t.Name == namespacedTool {
			visible = true
			break
		}
	}
	if !visible {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, namespacedTool)
	}

	sess, err := m.sessionFor(ctx, req.OrgID, req.UserID, server)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	result, err := sess.CallTool(cctx, bare, args)
	if err != nil {
		return nil, err
	}

	// Response hygiene: must be valid JSON within the size cap.
	if len(result) > m.maxResp {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrBadResponse, m.maxResp)
	}
	if !json.Valid(result) {
		return nil, ErrBadResponse
	}
	return result, nil
}

// Authz carries the caller identity + grant scopes for policy checks.
type Authz struct {
	OrgID  string
	UserID string
	Grants []string
}

// Namespace builds the collision-safe model-facing tool id.
func Namespace(server, tool string) string {
	return fmt.Sprintf("mcp__%s__%s", server, tool)
}

// SplitNamespace reverses Namespace.
func SplitNamespace(namespaced string) (server, tool string, ok bool) {
	rest := strings.TrimPrefix(namespaced, "mcp__")
	i := strings.Index(rest, "__")
	if i <= 0 || i == len(rest)-2 {
		return "", "", false
	}
	return rest[:i], rest[i+2:], true
}

func matchAny(globs []string, name string) bool {
	for _, g := range globs {
		if g == "*" || globMatch(g, name) {
			return true
		}
	}
	return false
}

// globMatch supports trailing-* prefixes ("issues.*") and exact matches.
func globMatch(pattern, name string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return pattern == name
}
