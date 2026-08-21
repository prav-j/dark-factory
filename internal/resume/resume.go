// Package resume implements the session resume flow (specs/16.4):
// manifest lookup -> branch verification -> fresh pod fork from the
// environment snapshot -> context hydration (transcript tail, summary, PR
// states, uncommitted-diff recovery) -> ready to continue the agent loop.
package resume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/prav-j/dark-factory/internal/warmpool"
)

var (
	ErrNoManifest = errors.New("session has no manifest to resume from")
)

// BranchStatus is the upstream state of a manifest branch.
type BranchStatus string

const (
	BranchExists BranchStatus = "exists"
	BranchGone   BranchStatus = "missing"
	BranchMerged BranchStatus = "merged"
)

// SessionLookup fetches a stored session (sessionstore-backed).
type SessionLookup interface {
	GetSession(ctx context.Context, orgID, sessionID string) (orgIDOut, environmentKey string, manifest []byte, err error)
}

// Manifest mirrors stophook.Manifest (loosely typed here to avoid coupling).
type Manifest struct {
	SessionID     string     `json:"sessionId"`
	AgentVersion  string     `json:"agentVersion"`
	TranscriptRef string     `json:"transcriptRef"`
	GitState      []GitEntry `json:"gitState"`
	EndedReason   string     `json:"endedReason"`
}

type GitEntry struct {
	Repo        string   `json:"repo"`
	Branch      string   `json:"branch"`
	HeadSHA     string   `json:"headSha"`
	PRs         []string `json:"prs"`
	Uncommitted bool     `json:"uncommitted"`
	DiffRef     string   `json:"diffRef,omitempty"`
}

// GitChecker verifies upstream branch state.
type GitChecker interface {
	Status(ctx context.Context, origin, branch, headSHA string) (BranchStatus, error)
}

// TranscriptReader hydrates prior conversation context.
type TranscriptReader interface {
	Summary(ctx context.Context, transcriptRef string) (string, error)
	Tail(ctx context.Context, transcriptRef string, n int) ([]Message, error)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// DiffFetcher retrieves recovery diffs from object storage.
type DiffFetcher interface {
	Fetch(ctx context.Context, ref string) ([]byte, error)
}

// Plan is everything needed to restart the agent loop in a fresh pod.
type Plan struct {
	SessionID      string
	PodID          string
	EnvironmentKey string
	WarmHit        bool
	AgentVersion   string
	Messages       []Message
	Summary        string
	OpenPRs        []string
	RecoveryDiff   []byte // non-nil when the previous session ended uncommitted
	Warnings       []string
}

// Resumer orchestrates the flow.
type Resumer struct {
	Sessions   SessionLookup
	Git        GitChecker
	Pools      *warmpool.Pool
	Transcript TranscriptReader
	Diffs      DiffFetcher
	TailN      int
}

// Resume executes steps 1-6 of specs/16.4; step 7 (continue the loop) is the
// caller's harness invocation on the returned plan.
func (r *Resumer) Resume(ctx context.Context, orgID, sessionID string) (*Plan, error) {
	if r.TailN == 0 {
		r.TailN = 20
	}
	orgOut, envKey, raw, err := r.Sessions.GetSession(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	_ = orgOut
	if len(raw) == 0 {
		return nil, ErrNoManifest
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest decode: %w", err)
	}

	plan := &Plan{
		SessionID:      sessionID,
		EnvironmentKey: envKey,
		AgentVersion:   m.AgentVersion,
	}

	// Step 2: resolve branches — flag gone/merged rather than failing.
	for _, g := range m.GitState {
		if g.HeadSHA == "" {
			continue
		}
		status, err := r.Git.Status(ctx, g.Repo, g.Branch, g.HeadSHA)
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("branch check %s@%s failed: %v", g.Branch, g.Repo, err))
			continue
		}
		switch status {
		case BranchGone:
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("branch %s no longer exists on %s", g.Branch, g.Repo))
		case BranchMerged:
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("branch %s was already merged (%v)", g.Branch, g.PRs))
			plan.OpenPRs = append(plan.OpenPRs, g.PRs...)
		default:
			plan.OpenPRs = append(plan.OpenPRs, g.PRs...)
		}
	}

	// Step 3: fresh pod from the same environment snapshot.
	podID, warm, err := r.Pools.Acquire(ctx, envKey, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox fork: %w", err)
	}
	plan.PodID = podID
	plan.WarmHit = warm

	// Steps 5+6: hydrate context; surface uncommitted recovery first.
	if m.TranscriptRef != "" && r.Transcript != nil {
		if s, err := r.Transcript.Summary(ctx, m.TranscriptRef); err == nil {
			plan.Summary = s
		}
		if msgs, err := r.Transcript.Tail(ctx, m.TranscriptRef, r.TailN); err == nil {
			plan.Messages = msgs
		}
	}
	for _, g := range m.GitState {
		if !g.Uncommitted || g.DiffRef == "" || r.Diffs == nil {
			continue
		}
		diff, err := r.Diffs.Fetch(ctx, g.DiffRef)
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("recovery diff %q unavailable: %v", g.DiffRef, err))
			continue
		}
		plan.RecoveryDiff = diff
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("previous session ended with UNCOMMITTED changes on %s (%s); review before proceeding", g.Branch, g.Repo))
	}
	return plan, nil
}
