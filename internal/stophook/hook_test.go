package stophook_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/stophook"
)

type fakePusher struct {
	failOrigins map[string]error
	pushed      []string
	headSHA     string
	prURL       string
}

func (f *fakePusher) CommitAndPush(_ context.Context, origin, branch string, _ []byte) (string, string, error) {
	if err, bad := f.failOrigins[origin]; bad {
		return "", "", err
	}
	f.pushed = append(f.pushed, origin+"@"+branch)
	return f.headSHA, f.prURL, nil
}

type memBlobs struct{ stored map[string][]byte }

func newBlobs() *memBlobs { return &memBlobs{stored: map[string][]byte{}} }

func (m *memBlobs) Upload(_ context.Context, key string, data []byte) (string, error) {
	m.stored[key] = data
	return "s3://blobs/" + key, nil
}

type memPersister struct{ manifests map[string][]byte }

func newPersister() *memPersister { return &memPersister{manifests: map[string][]byte{}} }

func (m *memPersister) SaveManifest(_ context.Context, _, sessionID string, manifest []byte) error {
	m.manifests[sessionID] = manifest
	return nil
}

func baseInput() stophook.Input {
	return stophook.Input{
		OrgID: "org-1", SessionID: "sess-1", AgentVersion: "bot@v7",
		TranscriptRef: "s3://transcripts/org-1/sess-1.jsonl",
		EndedReason:   "idle-timeout",
	}
}

func TestSuccessfulCommitProducesCleanManifest(t *testing.T) {
	pusher := &fakePusher{headSHA: "e3f1abc", prURL: "https://github.com/acme/api/pull/482"}
	blobs := newBlobs()
	persist := newPersister()
	h := stophook.New(pusher, blobs, persist, time.Second)

	in := baseInput()
	in.Repos = []stophook.RepoWork{{
		Origin: "https://github.com/acme/api", Branch: "agent/sess-1/fix-auth",
		Diff: []byte("diff --git ..."),
	}}

	m, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GitState) != 1 || m.GitState[0].Uncommitted {
		t.Fatalf("gitState = %+v", m.GitState)
	}
	if m.GitState[0].HeadSHA != "e3f1abc" || m.GitState[0].PRs[0] != "https://github.com/acme/api/pull/482" {
		t.Fatalf("gitState wrong: %+v", m.GitState[0])
	}
	if m.TranscriptRef == "" || m.EndedReason != "idle-timeout" {
		t.Fatalf("manifest incomplete: %+v", m)
	}
	if _, ok := persist.manifests["sess-1"]; !ok {
		t.Fatal("manifest must be persisted")
	}
	if len(blobs.stored) != 0 {
		t.Fatal("successful push should not upload diffs")
	}
}

// C16-004 groundwork: model fails to commit in time -> uncommitted:true plus
// a recovery diff blob; the manifest is still emitted.
func TestFailedCommitPreservesDiffBlob(t *testing.T) {
	pusher := &fakePusher{
		failOrigins: map[string]error{"https://github.com/acme/api": errors.New("push rejected")},
	}
	blobs := newBlobs()
	persist := newPersister()
	h := stophook.New(pusher, blobs, persist, time.Second)

	diff := []byte("diff --git a/main.go b/main.go\n+unpushed work")
	in := baseInput()
	in.Repos = []stophook.RepoWork{{
		Origin: "https://github.com/acme/api", Branch: "agent/sess-1/wip", Diff: diff,
	}}

	m, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GitState) != 1 || !m.GitState[0].Uncommitted {
		t.Fatalf("gitState = %+v, want uncommitted flag", m.GitState)
	}
	ref := m.GitState[0].DiffRef
	if ref == "" || !strings.Contains(string(blobs.stored[strings.TrimPrefix(ref, "s3://blobs/")]), "unpushed work") {
		t.Fatalf("recovery diff blob missing: ref=%q", ref)
	}
	if m.GitState[0].HeadSHA != "" {
		t.Fatal("failed push must not claim a head SHA")
	}
	if _, ok := persist.manifests["sess-1"]; !ok {
		t.Fatal("manifest emitted even on failure")
	}
}

func TestGracePeriodExhaustionStillEmitsManifest(t *testing.T) {
	slow := slowPusher{}
	blobs := newBlobs()
	persist := newPersister()
	h := stophook.New(slow, blobs, persist, 50*time.Millisecond)

	in := baseInput()
	in.EndedReason = "preemption"
	in.Repos = []stophook.RepoWork{{
		Origin: "https://github.com/acme/api", Branch: "b", Diff: []byte("wip"),
	}}
	m, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("grace exhaustion must not error the hook: %v", err)
	}
	if !m.GitState[0].Uncommitted || m.GitState[0].DiffRef == "" {
		t.Fatalf("fallback path wrong: %+v", m.GitState)
	}
	if m.EndedReason != "preemption" {
		t.Fatalf("endedReason lost: %q", m.EndedReason)
	}
}

type slowPusher struct{}

func (slowPusher) CommitAndPush(ctx context.Context, _, _ string, _ []byte) (string, string, error) {
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case <-time.After(5 * time.Second):
		return "sha", "", nil
	}
}
