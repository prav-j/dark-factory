//go:build conformance && integration

package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/prav-j/dark-factory/internal/harness"
	"github.com/prav-j/dark-factory/internal/runtoken"
)

// shared helpers for the security pack

func ctx() context.Context { return context.Background() }

type staticSessionsForPackType struct{}

func (staticSessionsForPackType) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)}, nil
}

func staticSessionsForPack() runtoken.SessionChecker {
	return staticSessionsForPackType{}
}

type noRevocationsForPack struct{}

func (noRevocationsForPack) Revoke(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (noRevocationsForPack) IsRevoked(_ context.Context, jti string) (bool, error) {
	return false, nil
}

// packRevocations is a real revocation list for the stolen-token check.
type packRevocations struct {
	revoked map[string]time.Time
}

func (p *packRevocations) Revoke(_ context.Context, jti string, ttl time.Duration) error {
	p.revoked[jti] = time.Now().Add(ttl)
	return nil
}
func (p *packRevocations) IsRevoked(_ context.Context, jti string) (bool, error) {
	until, ok := p.revoked[jti]
	if !ok || time.Now().After(until) {
		return false, nil
	}
	return true, nil
}

type packLoopTool struct{ executions int }

func (t *packLoopTool) Name() string           { return "loop" }
func (t *packLoopTool) Description() string    { return "" }
func (t *packLoopTool) InputSchema() string    { return "{}" }
func (t *packLoopTool) RequiresApproval() bool { return false }
func (t *packLoopTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	t.executions++
	return "again", nil
}

type packInfiniteCompleter struct{}

func (packInfiniteCompleter) Complete(_ context.Context, _ harness.CompletionRequest) (*harness.Completion, error) {
	return &harness.Completion{StopReason: "tool_use", ToolName: "loop", ToolInput: `{}`}, nil
}

type packCheckpointer struct{ states map[string]*harness.RunState }

func newPackCheckpointer() *packCheckpointer {
	return &packCheckpointer{states: map[string]*harness.RunState{}}
}
func (m *packCheckpointer) Save(_ context.Context, id string, s *harness.RunState) error {
	cp := *s
	m.states[id] = &cp
	return nil
}
func (m *packCheckpointer) Load(_ context.Context, id string) (*harness.RunState, error) {
	s, ok := m.states[id]
	if !ok {
		return nil, errors.New("missing")
	}
	return s, nil
}
func (m *packCheckpointer) Delete(_ context.Context, id string) error {
	delete(m.states, id)
	return nil
}
