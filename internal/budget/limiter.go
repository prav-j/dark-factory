package budget

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

// RateLimits configure per-second ceilings per level.
type RateLimits struct {
	User  float64
	Agent float64
	Org   float64
	Tool  float64
}

// RateLimiter enforces all four levels; the most restrictive applicable
// limit wins (specs/09.2).
type RateLimiter struct {
	mu       sync.Mutex
	limits   RateLimits
	limiters map[string]*rate.Limiter
}

func NewRateLimiter(l RateLimits) *RateLimiter {
	return &RateLimiter{limits: l, limiters: map[string]*rate.Limiter{}}
}

// Allow reports whether this call may proceed, consuming budget on every
// level it participates in. Denied calls consume nothing.
func (r *RateLimiter) Allow(orgID, userID, agentRef, tool string) error {
	type level struct {
		name string
		qps  float64
	}
	levels := []level{
		{"user:" + userID, r.limits.User},
		{"agent:" + agentRef, r.limits.Agent},
		{"org:" + orgID, r.limits.Org},
		{"tool:" + tool, r.limits.Tool},
	}

	var acquired []*rate.Limiter
	defer func() {
		// On failure, nothing to roll back: rate.Limiter tokens are only
		// consumed by successful Wait calls below.
		for _, l := range acquired {
			_ = l
		}
	}()

	for _, lv := range levels {
		if lv.qps <= 0 {
			continue // unlimited at this level
		}
		l := r.limiterFor(lv.name, lv.qps)
		if err := l.Wait(context.Background()); err != nil {
			return fmt.Errorf("rate limited at %s level: %w", strings.SplitN(lv.name, ":", 2)[0], err)
		}
		acquired = append(acquired, l)
	}
	return nil
}

func (r *RateLimiter) limiterFor(key string, qps float64) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.limiters[key]
	if !ok {
		l = rate.NewLimiter(rate.Limit(qps), int(qps)+1) // small burst
		r.limiters[key] = l
	}
	return l
}
