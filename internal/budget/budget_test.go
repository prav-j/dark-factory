package budget_test

import (
	"context"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/budget"
	"github.com/prav-j/dark-factory/internal/modelgw"
)

type recordingAlerter struct {
	alerts []budget.Alert
}

func (r *recordingAlerter) Fire(_ context.Context, a budget.Alert) {
	r.alerts = append(r.alerts, a)
}

func TestHardBudgetRejection(t *testing.T) {
	tracker := budget.NewTracker(budget.Limits{UserMonthlyUSD: 10.0}, nil)
	org := "org-1"

	// Under limit: allowed.
	if err := tracker.CheckBudget(org, "user-1"); err != nil {
		t.Fatalf("under-limit check failed: %v", err)
	}
	tracker.AddSpend(context.Background(), org, "user-1", 10.01) // hard over

	err := tracker.CheckBudget(org, "user-1")
	if err == nil {
		t.Fatalf("over-budget check = %v", err)
	}
	// Org under its (unlimited) org budget is irrelevant: user check rejects.
}

// Alert fires exactly once when crossing 80%.
func TestSoftAlertFiredOnceAtThreshold(t *testing.T) {
	al := &recordingAlerter{}
	tracker := budget.NewTracker(budget.Limits{UserMonthlyUSD: 100.0}, al)
	ctx := context.Background()

	tracker.AddSpend(ctx, "org-1", "u1", 50) // below threshold
	if len(al.alerts) != 0 {
		t.Fatalf("premature alert: %+v", al.alerts)
	}
	tracker.AddSpend(ctx, "org-1", "u1", 35) // crosses 80
	if len(al.alerts) != 1 {
		t.Fatalf("alerts = %d, want exactly 1 at threshold", len(al.alerts))
	}
	tracker.AddSpend(ctx, "org-1", "u1", 30) // well past; no repeat
	tracker.AddSpend(ctx, "org-1", "u1", 30)
	if len(al.alerts) != 1 {
		t.Fatalf("alert must fire once per period, got %d", len(al.alerts))
	}
	a := al.alerts[0]
	if a.SpendUSD < a.LimitUSD*0.8 || a.Scope != "user:u1" {
		t.Fatalf("alert content wrong: %+v", a)
	}
}

// Metering through the modelgw.Meter interface attributes to both scopes.
func TestGatewayMeteringAttribution(t *testing.T) {
	tracker := budget.NewTracker(budget.Limits{}, nil)
	tracker.Record(context.Background(), modelgw.UsageRecord{
		RunID: "r1", UserID: "u1", OrgID: "org-9", CostUSD: 2.5,
	})
	if got := tracker.Spend("user:u1"); got != 2.5 {
		t.Fatalf("user spend = %f", got)
	}
	if got := tracker.Spend("org:org-9"); got != 2.5 {
		t.Fatalf("org spend = %f", got)
	}
}

func TestRateLimitsAtEachLevel(t *testing.T) {
	rl := budget.NewRateLimiter(budget.RateLimits{
		User:  5,
		Agent: 4,
		Org:   3,
		Tool:  2,
	})
	deadline := time.Now().Add(150 * time.Millisecond)
	calls := 0
	for time.Now().Before(deadline) {
		if err := rl.Allow("org-1", "u", "agent@v1", "tool"); err != nil {
			break // most restrictive level kicked in
		}
		calls++
	}
	if calls > 6 { // tool qps=2 with burst ~3 + scheduling slack
		t.Fatalf("tool-level limit not enforced: %d immediate calls", calls)
	}
	if calls == 0 {
		t.Fatal("limiters rejected everything")
	}
}
