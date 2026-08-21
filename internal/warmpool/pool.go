// Package warmpool manages pools of pre-forked session pods keyed by
// environment snapshot cache key (specs/09, specs/16). The K8s operator owns
// the real lifecycle as low-priority deployments promoted into sessions;
// this package is the policy layer: warm-hit vs cold-fork accounting,
// replenishment targets, and idle scale-down.
package warmpool

import (
	"context"
	"sync"
	"time"
)

// Forker abstracts pod lifecycle (operator-backed in prod).
type Forker interface {
	// Fork creates a pre-warmed pod from the snapshot for environmentKey.
	Fork(ctx context.Context, environmentKey string) (podID string, err error)
	// Promote attaches the session's COW overlay and hands ownership over.
	Promote(ctx context.Context, podID, sessionID string) error
	// Destroy kills a pooled pod that was never promoted.
	Destroy(ctx context.Context, podID string) error
}

type pooled struct {
	podID    string
	forkedAt time.Time
}

// Pool tracks pre-forked pods per environmentKey.
type Pool struct {
	forker     Forker
	maxPerKey  int
	idleMaxAge time.Duration

	mu    sync.Mutex
	pools map[string][]pooled
	clock func() time.Time
}

func New(f Forker, maxPerKey int, idleMaxAge time.Duration) *Pool {
	return &Pool{
		forker:     f,
		maxPerKey:  maxPerKey,
		idleMaxAge: idleMaxAge,
		pools:      map[string][]pooled{},
		clock:      time.Now,
	}
}

// SetClock overrides time (tests).
func (p *Pool) SetClock(c func() time.Time) { p.clock = c }

// Acquire returns a warm pod when one is pooled (warm hit), otherwise forks
// fresh (cold path). Either way the returned pod is promoted to the session
// with a fresh writable overlay.
func (p *Pool) Acquire(ctx context.Context, environmentKey, sessionID string) (podID string, warm bool, err error) {
	p.mu.Lock()
	if q := p.pools[environmentKey]; len(q) > 0 {
		entry := q[0]
		p.pools[environmentKey] = q[1:]
		p.mu.Unlock()

		return entry.podID, true, p.forker.Promote(ctx, entry.podID, sessionID)
	}
	p.mu.Unlock()

	cold, err := p.forker.Fork(ctx, environmentKey)
	if err != nil {
		return "", false, err
	}
	return cold, false, p.forker.Promote(ctx, cold, sessionID)
}

// Replenish tops a key's pool back up to its target size, respecting the
// per-key cap. Returns how many pods were newly forked.
func (p *Pool) Replenish(ctx context.Context, environmentKey string, target int) (int, error) {
	if target > p.maxPerKey {
		target = p.maxPerKey
	}
	p.mu.Lock()
	current := len(p.pools[environmentKey])
	p.mu.Unlock()

	forked := 0
	for current+forked < target {
		id, err := p.forker.Fork(ctx, environmentKey)
		if err != nil {
			return forked, err
		}
		p.mu.Lock()
		p.pools[environmentKey] = append(p.pools[environmentKey], pooled{podID: id, forkedAt: p.clock()})
		p.mu.Unlock()
		forked++
	}
	return forked, nil
}

// ScaleDown destroys pooled pods older than idleMaxAge. Returns destroyed ids.
func (p *Pool) ScaleDown(ctx context.Context) ([]string, error) {
	now := p.clock()
	var destroyed []string

	p.mu.Lock()
	for key, q := range p.pools {
		var keep []pooled
		for _, e := range q {
			if now.Sub(e.forkedAt) >= p.idleMaxAge {
				destroyed = append(destroyed, e.podID)
				continue
			}
			keep = append(keep, e)
		}
		p.pools[key] = keep
	}
	p.mu.Unlock()

	for _, id := range destroyed {
		if err := p.forker.Destroy(ctx, id); err != nil {
			return destroyed, err
		}
	}
	return destroyed, nil
}

// Size reports the pooled count for a key.
func (p *Pool) Size(environmentKey string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pools[environmentKey])
}
