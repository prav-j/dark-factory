// Package budget implements spend metering aggregation and the four-level
// rate limiter (user, agent, org, tool) from specs/09.2: hard budget
// rejection before allocation and soft alerts fired once at 80%.
package budget

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prav-j/dark-factory/internal/modelgw"
)

// Limits are monthly USD budgets per level; zero means unlimited (still
// tracked).
type Limits struct {
	UserMonthlyUSD float64
	OrgMonthlyUSD  float64
}

// Alert is a soft budget notification (fired exactly once per period).
type Alert struct {
	Scope    string    `json:"scope"` // user:user-1 | org:org-1
	SpendUSD float64   `json:"spendUsd"`
	LimitUSD float64   `json:"limitUsd"`
	FiredAt  time.Time `json:"firedAt"`
}

// Alerter receives soft-budget alerts.
type Alerter interface {
	Fire(ctx context.Context, a Alert)
}

// Tracker aggregates metered usage and enforces budgets. It satisfies
// modelgw.Meter so the gateway can feed it directly.
type Tracker struct {
	Limits  Limits
	Alerter Alerter

	mu      sync.Mutex
	spend   map[string]float64 // scope -> USD this period
	alerted map[string]bool    // scope -> alert fired this period
}

func NewTracker(limits Limits, alerter Alerter) *Tracker {
	return &Tracker{
		Limits: limits, Alerter: alerter,
		spend:   map[string]float64{},
		alerted: map[string]bool{},
	}
}

// Record attributes gateway usage to both user and org scopes.
func (t *Tracker) Record(ctx context.Context, u modelgw.UsageRecord) {
	t.AddSpend(ctx, u.OrgID, u.UserID, u.CostUSD)
}

// AddSpend attributes cost explicitly; alerts fire once when a scope crosses
// 80% of its limit.
func (t *Tracker) AddSpend(ctx context.Context, orgID, userID string, usd float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.addLocked(ctx, "user:"+userID, usd)
	t.addLocked(ctx, "org:"+orgID, usd)
}

// addLocked must hold mu.
func (t *Tracker) addLocked(_ context.Context, scope string, usd float64) {
	t.spend[scope] += usd

	var limit float64
	if len(scope) > 5 && scope[:5] == "user:" {
		limit = t.Limits.UserMonthlyUSD
	} else if len(scope) > 4 && scope[:4] == "org:" {
		limit = t.Limits.OrgMonthlyUSD
	}
	if limit <= 0 || t.alerted[scope] || t.Alerter == nil {
		return
	}
	prev := t.spend[scope] - usd
	now := t.spend[scope]
	if now >= limit*0.8 && prev < limit*0.8 {
		t.alerted[scope] = true
		t.Alerter.Fire(context.Background(), Alert{
			Scope: scope, SpendUSD: now, LimitUSD: limit, FiredAt: time.Now(),
		})
	}
}

// CheckBudget reports whether the scopes remain under hard limits.
func (t *Tracker) CheckBudget(orgID, userID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if l := t.Limits.UserMonthlyUSD; l > 0 && t.spend["user:"+userID] >= l {
		return fmt.Errorf("user %s: monthly budget exhausted (%.2f USD spent)", userID, t.spend["user:"+userID])
	}
	if l := t.Limits.OrgMonthlyUSD; l > 0 && t.spend["org:"+orgID] >= l {
		return fmt.Errorf("org %s: monthly budget exhausted (%.2f USD spent)", orgID, t.spend["org:"+orgID])
	}
	return nil
}

// Spend reports current period spend for a scope.
func (t *Tracker) Spend(scope string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.spend[scope]
}

var _ modelgw.Meter = (*Tracker)(nil)
