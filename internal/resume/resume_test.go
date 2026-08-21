package resume_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/resume"
	"github.com/prav-j/dark-factory/internal/warmpool"
)

const manifest = `{
  "sessionId": "sess-1",
  "agentVersion": "bot@v7",
  "transcriptRef": "s3://t/sess-1.jsonl",
  "gitState": [
    {"repo": "acme/api", "branch": "agent/sess-1/fix-auth", "headSha": "e3f1", "prs": ["#482"], "uncommitted": false}
  ],
  "endedReason": "idle-timeout"
}`

type fakeSessions struct {
	org, envKey string
	manifest    []byte
}

func (f *fakeSessions) GetSession(_ context.Context, orgID, _ string) (string, string, []byte, error) {
	return f.org, f.envKey, f.manifest, nil
}

type fakeGit struct {
	statuses map[string]resume.BranchStatus
}

func (f *fakeGit) Status(_ context.Context, _, branch, _ string) (resume.BranchStatus, error) {
	if s, ok := f.statuses[branch]; ok {
		return s, nil
	}
	return resume.BranchExists, nil
}

type fakeTranscript struct{}

func (fakeTranscript) Summary(_ context.Context, _ string) (string, error) {
	return "Was fixing auth middleware in acme/api.", nil
}
func (fakeTranscript) Tail(_ context.Context, _ string, n int) ([]resume.Message, error) {
	msgs := []resume.Message{
		{Role: "user", Content: "start"},
		{Role: "assistant", Content: "working on auth"},
		{Role: "user", Content: "keep going"},
	}
	if n < len(msgs) {
		msgs = msgs[len(msgs)-n:]
	}
	return msgs, nil
}

type fakeDiffs struct{ data map[string][]byte }

func (f *fakeDiffs) Fetch(_ context.Context, ref string) ([]byte, error) {
	if d, ok := f.data[ref]; ok {
		return d, nil
	}
	return nil, errors.New("missing blob")
}

var errForkFailed = errors.New("fork failed")

// reuse warmpool via its Forker interface
type forker struct {
	mu       sync.Mutex
	forked   int
	promoted map[string]string
	fail     bool
}

func (f *forker) Fork(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", errForkFailed
	}
	f.forked++
	return fmt.Sprintf("%s-pod-%d", key, f.forked), nil
}
func (f *forker) Promote(_ context.Context, podID, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promoted[podID] = sessionID
	return nil
}
func (f *forker) Destroy(_ context.Context, _ string) error { return nil }

func setup(t *testing.T, manifestBytes []byte, statuses map[string]resume.BranchStatus, diffs map[string][]byte) (*resume.Resumer, *forker) {
	t.Helper()
	fk := &forker{promoted: map[string]string{}}
	pool := warmpool.New(fk, 4, 30*time.Minute)
	return &resume.Resumer{
		Sessions:   &fakeSessions{org: "org-1", envKey: "snap-abc", manifest: manifestBytes},
		Git:        &fakeGit{statuses: statuses},
		Pools:      pool,
		Transcript: fakeTranscript{},
		Diffs:      &fakeDiffs{data: diffs},
	}, fk
}

func TestResumeHappyPath(t *testing.T) {
	r, forked := setup(t, []byte(manifest), nil, nil)
	plan, err := r.Resume(context.Background(), "org-1", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if plan.EnvironmentKey != "snap-abc" || plan.PodID == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if forked.forked != 1 || !strings.Contains(plan.PodID, "snap-abc-pod") {
		t.Fatalf("fresh fork missing: %+v", forked)
	}
	if plan.Summary == "" || len(plan.Messages) != 3 {
		t.Fatalf("context hydration wrong: summary=%q messages=%d", plan.Summary, len(plan.Messages))
	}
	for _, pr := range plan.OpenPRs {
		if pr != "#482" {
			t.Fatalf("open PRs = %v", plan.OpenPRs)
		}
	}
}

func TestResumeSurfacesUncommittedRecovery(t *testing.T) {
	m := `{"sessionId":"s","agentVersion":"bot@v1","transcriptRef":"s3://t/x.jsonl",
	  "gitState":[{"repo":"acme/api","branch":"agent/s/wip","headSha":"","prs":[],
	    "uncommitted":true,"diffRef":"s3://blobs/uncommitted.diff"}],
	  "endedReason":"max-lifetime"}`
	diffs := map[string][]byte{"s3://blobs/uncommitted.diff": []byte("unpushed changes here")}
	r, _ := setup(t, []byte(m), nil, diffs)

	plan, err := r.Resume(context.Background(), "org-1", "s")
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecoveryDiff == nil || !strings.Contains(string(plan.RecoveryDiff), "unpushed") {
		t.Fatalf("recovery diff not surfaced: %+v", plan.RecoveryDiff)
	}
	found := false
	for _, w := range plan.Warnings {
		if strings.Contains(w, "UNCOMMITTED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warning not surfaced: %v", plan.Warnings)
	}
}

func TestResumeHandlesMergedAndMissingBranches(t *testing.T) {
	m := `{"sessionId":"s","agentVersion":"bot@v1","transcriptRef":"s3://t/x.jsonl",
	  "gitState":[
	    {"repo":"a","branch":"merged-work","headSha":"sha1","prs":["#10"],"uncommitted":false},
	    {"repo":"b","branch":"vanished","headSha":"sha2","prs":[],"uncommitted":false}],
	  "endedReason":"idle-timeout"}`
	statuses := map[string]resume.BranchStatus{
		"merged-work": resume.BranchMerged,
		"vanished":    resume.BranchGone,
	}
	r, _ := setup(t, []byte(m), statuses, nil)

	plan, err := r.Resume(context.Background(), "org-1", "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 2 {
		t.Fatalf("warnings = %v, want merged + missing notes", plan.Warnings)
	}
}

func TestResumeWithoutManifestRejected(t *testing.T) {
	r, _ := setup(t, nil, nil, nil)
	if _, err := r.Resume(context.Background(), "org-1", "s"); err == nil ||
		!strings.Contains(err.Error(), "manifest") {
		t.Fatalf("err = %v, want no-manifest rejection", err)
	}
}
