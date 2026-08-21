//go:build conformance && integration

package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/prav-j/dark-factory/internal/harness"
	"github.com/prav-j/dark-factory/internal/orchestrator"
	"github.com/prav-j/dark-factory/internal/resume"
	"github.com/prav-j/dark-factory/internal/stophook"
	"github.com/prav-j/dark-factory/internal/testutil"
	"github.com/prav-j/dark-factory/internal/warmpool"
	"time"
)

// --- harness fakes (mirrors internal/harness test doubles) ---

type c05Completer struct {
	script []harness.Completion
	calls  int
	specs  [][]harness.ToolSpec
}

func (c *c05Completer) Complete(_ context.Context, req harness.CompletionRequest) (*harness.Completion, error) {
	if c.calls >= len(c.script) {
		return nil, errors.New("script exhausted")
	}
	c.specs = append(c.specs, req.Tools)
	out := c.script[c.calls]
	c.calls++
	return &out, nil
}

type c05Tool struct {
	name     string
	approval bool
	ran      int
}

func (t *c05Tool) Name() string           { return t.name }
func (t *c05Tool) Description() string    { return "" }
func (t *c05Tool) InputSchema() string    { return "{}" }
func (t *c05Tool) RequiresApproval() bool { return t.approval }
func (t *c05Tool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	t.ran++
	return "ok", nil
}

type c05Checkpointer struct{ states map[string]*harness.RunState }

func newC05Checkpointer() *c05Checkpointer {
	return &c05Checkpointer{states: map[string]*harness.RunState{}}
}
func (m *c05Checkpointer) Save(_ context.Context, id string, s *harness.RunState) error {
	cp := *s
	m.states[id] = &cp
	return nil
}
func (m *c05Checkpointer) Load(_ context.Context, id string) (*harness.RunState, error) {
	s, ok := m.states[id]
	if !ok {
		return nil, errors.New("missing")
	}
	return s, nil
}
func (m *c05Checkpointer) Delete(_ context.Context, id string) error {
	delete(m.states, id)
	return nil
}

// C05-001 — only tools in the effective grant set are exposed to the model.
func TestC05001OnlyGrantedToolsExposed(t *testing.T) {
	granted := &c05Tool{name: "search"}
	ungranted := &c05Tool{name: "deploy_prod", approval: true}
	completer := &c05Completer{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "search", ToolInput: `{}`},
		{StopReason: "end_turn", Text: "done"},
	}}
	h := harness.New(completer, []harness.Tool{granted, ungranted}, 10, newC05Checkpointer())

	state, err := h.Run(ctx(), &harness.RunState{
		RunID: "r", Messages: []harness.Message{{Role: "user", Content: "go"}},
	}, []string{"search"}, "m")
	if err != nil || state.FinalText != "done" {
		t.Fatalf("run: %+v err %v", state, err)
	}
	for i, specs := range completer.specs {
		if len(specs) != 1 || specs[0].Name != "search" {
			t.Fatalf("call %d exposed %+v; ungranted tool leaked", i, specs)
		}
	}
	Pass(t, Check{ID: "C05-001", Spec: "05-execution-flow.md#agent-loop-runtime",
		Text: "Context assembly exposes only tools in the effective grant set to the model."})
}

// C05-002 — requiresApproval pauses; approve executes and completes.
func TestC05002ApprovalGates(t *testing.T) {
	deploy := &c05Tool{name: "deploy", approval: true}
	h := harness.New(&c05Completer{script: []harness.Completion{
		{StopReason: "tool_use", ToolName: "deploy", ToolInput: `{}`},
		{StopReason: "end_turn", Text: "shipped"},
	}}, []harness.Tool{deploy}, 10, newC05Checkpointer())

	state, err := h.Run(ctx(), &harness.RunState{
		RunID: "r2", Messages: []harness.Message{{Role: "user", Content: "ship"}},
	}, []string{"deploy"}, "m")
	if err != nil || state.Status != "awaiting_approval" || deploy.ran != 0 {
		t.Fatalf("pause wrong: %+v ran=%d err=%v", state.Status, deploy.ran, err)
	}
	final, err := h.Resume(ctx(), "r2", true, "m")
	if err != nil || final.FinalText != "shipped" || deploy.ran != 1 {
		t.Fatalf("resume wrong: %+v ran=%d err=%v", final, deploy.ran, err)
	}
	Pass(t, Check{ID: "C05-002", Spec: "05-execution-flow.md#agent-loop-runtime",
		Text: "Tools marked requiresApproval pause the run pending user decision; run resumes on approve/deny."})
}

// C05-003 — git-durable persistence: end-of-session state is the manifest
// (transcript + branch refs); resume needs nothing else. No filesystem
// checkpoint exists.
func TestC05003GitIsDurableWorkspace(t *testing.T) {
	pusher := &c05Pusher{}
	blobs := newBlobs()
	persist := newPersister()
	hook := stophook.New(pusher, blobs, persist, time.Second)

	in := stophook.Input{
		OrgID: "org", SessionID: "sess-c5", AgentVersion: "bot@v1",
		TranscriptRef: "s3://t/x.jsonl", EndedReason: "idle-timeout",
		Repos: []stophook.RepoWork{{Origin: "acme/api", Branch: "agent/sess-c5/w", Diff: []byte("d")}},
	}
	m, err := hook.Run(ctx(), in)
	if err != nil || len(m.GitState) != 1 || m.GitState[0].Uncommitted {
		t.Fatalf("manifest = %+v err %v", m, err)
	}

	// Resume consumes ONLY manifest data (no filesystem state).
	resumer := &resume.Resumer{
		Sessions:   &c05SessionLookup{manifest: mustJSON(m)},
		Git:        &c05Git{},
		Pools:      warmpoolForTest(),
		Transcript: c05Transcript{},
		TailN:      10,
	}
	plan, err := resumer.Resume(ctx(), "org", "sess-c5")
	if err != nil || plan.PodID == "" {
		t.Fatalf("plan = %+v err %v", plan, err)
	}
	if plan.Summary == "" || len(plan.Messages) == 0 {
		t.Fatalf("context not hydrated: summary=%q messages=%d", plan.Summary, len(plan.Messages))
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", plan.Warnings)
	}
	Pass(t, Check{ID: "C05-003", Spec: "05-execution-flow.md#persistence-model",
		Text: "Durable state is git branches/PRs + transcript + session manifest; sandbox overlays are always discarded."})
}

// C05-004 — webhook idempotency + budget-before-start (orchestrator).
func TestC05004WebhookIdempotencyAndBudget(t *testing.T) {
	rdb := testutil.Redis(t)
	budget := &c05Budget{}
	orch := orchestrator.New(rdb, 4, budget)

	req := orchestrator.RunRequest{
		RunID: "orig", SessionID: "s", UserID: "u", OrgID: "org",
		Priority: orchestrator.Background, IdempotencyKey: "dlv-1",
	}
	id1, err := orch.Enqueue(ctx(), req)
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := orch.Enqueue(ctx(), req)
	if id1 != id2 || id1 != "orig" {
		t.Fatalf("idempotent replay = %q/%q", id1, id2)
	}

	budget.over = true
	if _, err := orch.Enqueue(ctx(), orchestrator.RunRequest{
		RunID: "r2", SessionID: "s", UserID: "broke", OrgID: "org",
	}); !errors.Is(err, orchestrator.ErrBudgetExceeded) {
		t.Fatalf("budget gate: %v", err)
	}
	Pass(t, Check{ID: "C05-004", Spec: "05-execution-flow.md#autonomous-runs-scheduledwebhook",
		Text: "Webhook triggers are idempotent via idempotency keys; budget checked before start."})
}

// --- small helpers ---

type c05Budget struct{ over bool }

func (b *c05Budget) CheckBudget(_ context.Context, _, userID string) error {
	if b.over && userID == "broke" {
		return errors.New("over monthly limit")
	}
	return nil
}

// --- small helpers for C05-003 ---

type c05Blobs struct{ stored map[string][]byte }

func newBlobs() *c05Blobs { return &c05Blobs{stored: map[string][]byte{}} }
func (m *c05Blobs) Upload(_ context.Context, key string, data []byte) (string, error) {
	m.stored[key] = data
	return "s3://blobs/" + key, nil
}

type c05Persister struct{ manifests map[string][]byte }

func newPersister() *c05Persister { return &c05Persister{manifests: map[string][]byte{}} }
func (m *c05Persister) SaveManifest(_ context.Context, _, sessionID string, manifest []byte) error {
	m.manifests[sessionID] = manifest
	return nil
}

type c05Pusher struct{}

func (c05Pusher) CommitAndPush(context.Context, string, string, []byte) (string, string, error) {
	return "sha123", "", nil
}

type c05SessionLookup struct{ manifest []byte }

func (c *c05SessionLookup) GetSession(_ context.Context, _, _ string) (string, string, []byte, error) {
	return "org", "snap-k", c.manifest, nil
}

type c05Git struct{}

func (c05Git) Status(_ context.Context, _, _, _ string) (resume.BranchStatus, error) {
	return resume.BranchExists, nil
}

type c05Transcript struct{}

func (c05Transcript) Summary(_ context.Context, _ string) (string, error) { return "summary", nil }
func (c05Transcript) Tail(_ context.Context, _ string, n int) ([]resume.Message, error) {
	return []resume.Message{{Role: "user", Content: "last"}}, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

type c05Forker struct{ forks int }

func (f *c05Forker) Fork(_ context.Context, key string) (string, error) {
	f.forks++
	return key + "-pod-1", nil
}
func (f *c05Forker) Promote(_ context.Context, _, _ string) error { return nil }
func (f *c05Forker) Destroy(_ context.Context, _ string) error    { return nil }

func warmpoolForTest() *warmpool.Pool { return warmpool.New(&c05Forker{}, 4, time.Minute) }
