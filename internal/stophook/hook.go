// Package stophook implements the commit-before-death contract (specs/16.2):
// on any session end the hook instructs persistence of work to git, waits
// within its grace period, then emits the Session Manifest that makes resume
// possible with only the transcript + branch/PR refs. The sandbox overlay is
// disposable by design.
package stophook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// RepoWork is one repository touched during the session.
type RepoWork struct {
	Origin string `json:"origin"`
	Branch string `json:"branch"`
	Diff   []byte `json:"-"` // working-tree diff if the model left changes unpushed
}

// GitPusher commits and pushes pending work on branch, returning head SHA
// and PR URL if one was opened/updated.
type GitPusher interface {
	CommitAndPush(ctx context.Context, origin, branch string, diff []byte) (headSHA, prURL string, err error)
}

// BlobStore persists fallback artifacts (uncommitted diffs) to object storage.
type BlobStore interface {
	Upload(ctx context.Context, key string, data []byte) (ref string, err error)
}

// Persister stores the final manifest (sessionstore-backed in prod).
type Persister interface {
	SaveManifest(ctx context.Context, orgID, sessionID string, manifest []byte) error
}

// Manifest is the durable session summary (specs/16.2).
type Manifest struct {
	SessionID     string     `json:"sessionId"`
	AgentVersion  string     `json:"agentVersion"`
	TranscriptRef string     `json:"transcriptRef"`
	GitState      []GitState `json:"gitState"`
	EndedReason   string     `json:"endedReason"`
}

type GitState struct {
	Repo        string   `json:"repo"`
	Branch      string   `json:"branch"`
	HeadSHA     string   `json:"headSha"`
	PRs         []string `json:"prs"`
	Uncommitted bool     `json:"uncommitted"`
	DiffRef     string   `json:"diffRef,omitempty"` // recovery blob when Uncommitted
}

// Input describes what the hook needs at shutdown.
type Input struct {
	OrgID         string
	SessionID     string
	AgentVersion  string
	TranscriptRef string
	EndedReason   string // idle-timeout | max-lifetime | user-stop | preemption
	Repos         []RepoWork
}

// Hook executes the contract.
type Hook struct {
	pusher GitPusher
	blobs  BlobStore
	save   Persister
	grace  time.Duration
}

func New(pusher GitPusher, blobs BlobStore, save Persister, grace time.Duration) *Hook {
	if grace == 0 {
		grace = 120 * time.Second
	}
	return &Hook{pusher: pusher, blobs: blobs, save: save, grace: grace}
}

// Run performs the stop sequence: best-effort commit/push per repo within
// the grace period, diff-blob fallback for failures, manifest emission.
// It always produces a manifest unless the context is already dead.
func (h *Hook) Run(ctx context.Context, in Input) (*Manifest, error) {
	cctx, cancel := context.WithTimeout(ctx, h.grace)
	defer cancel()

	m := &Manifest{
		SessionID:     in.SessionID,
		AgentVersion:  in.AgentVersion,
		TranscriptRef: in.TranscriptRef,
		EndedReason:   in.EndedReason,
	}

	for _, rw := range in.Repos {
		gs := GitState{Repo: rw.Origin, Branch: rw.Branch}
		headSHA, prURL, err := h.pusher.CommitAndPush(cctx, rw.Origin, rw.Branch, rw.Diff)

		switch {
		case err == nil:
			gs.HeadSHA = headSHA
			if prURL != "" {
				gs.PRs = []string{prURL}
			}
		case errorsIs(err, context.DeadlineExceeded) || cctx.Err() != nil:
			// Grace exhausted: preserve the diff for next-resume recovery.
			ref, upErr := h.blobs.Upload(ctx, fmt.Sprintf("uncommitted/%s/%s.diff", in.SessionID, sanitize(rw.Origin)), rw.Diff)
			if upErr != nil {
				return nil, upErr
			}
			gs.Uncommitted = true
			gs.DiffRef = ref
			m.GitState = append(m.GitState, gs)
			return h.emit(ctx, in, m)
		default:
			ref, upErr := h.blobs.Upload(ctx, fmt.Sprintf("uncommitted/%s/%s.diff", in.SessionID, sanitize(rw.Origin)), rw.Diff)
			if upErr != nil {
				return nil, upErr
			}
			gs.Uncommitted = true
			gs.DiffRef = ref
		}
		m.GitState = append(m.GitState, gs)
	}
	return h.emit(ctx, in, m)
}

func (h *Hook) emit(ctx context.Context, in Input, m *Manifest) (*Manifest, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if h.save != nil {
		if err := h.save.SaveManifest(ctx, in.OrgID, in.SessionID, raw); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func sanitize(origin string) string {
	out := make([]byte, 0, len(origin))
	for _, c := range origin {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			out = append(out, byte(c))
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}
