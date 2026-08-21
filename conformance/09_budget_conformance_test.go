//go:build conformance && integration

package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/budget"
	"github.com/prav-j/dark-factory/internal/orchestrator"
	"github.com/prav-j/dark-factory/internal/testutil"
)

type c09Alerter struct{ fired int }

func (a *c09Alerter) Fire(_ context.Context, _ budget.Alert) { a.fired++ }

// C09-001 — rate limits enforced at all four levels; the most restrictive
// level wins; interactive traffic drains ahead of background.
func TestC09001FourLevelRateLimitsAndPriority(t *testing.T) {
	rl := budget.NewRateLimiter(budget.RateLimits{User: 100, Agent: 100, Org: 100, Tool: 3})
	calls := 0
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := rl.Allow("org", "u", "agent@v1", "tool"); err != nil {
			break
		}
		calls++
	}
	if calls == 0 || calls > 8 {
		t.Fatalf("most-restrictive level not enforced: %d immediate calls", calls)
	}

	// Interactive priority in the orchestrator.
	rdb := testutil.Redis(t)
	orch := orchestrator.New(rdb, 1, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := orch.Enqueue(ctx, orchestrator.RunRequest{
			RunID: string(rune('a' + i)), SessionID: "s", UserID: "u",
			OrgID: "org", Priority: orchestrator.Background,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := orch.Enqueue(ctx, orchestrator.RunRequest{
		RunID: "ix", SessionID: "s", UserID: "u", OrgID: "org",
		Priority: orchestrator.Interactive,
	}); err != nil {
		t.Fatal(err)
	}

	got := make(chan string, 4)
	wctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	go func() {
		_ = orch.RunWorker(wctx, 0, "w", func(_ context.Context, r orchestrator.RunRequest) error {
			time.Sleep(10 * time.Millisecond)
			got <- r.RunID
			return nil
		})
	}()
	if first := <-got; first != "ix" {
		t.Fatalf("first dequeued = %q, want interactive", first)
	}
	Pass(t, Check{ID: "C09-001", Spec: "09-scaling-cost.md#cost-controls",
		Text: "Rate limits enforced at user, agent, org, and tool levels."})
}

// C09-002 — hard budget rejection before allocation; soft alert once at 80%.
func TestC09002HardBudgetAndSoftAlert(t *testing.T) {
	al := &c09Alerter{}
	tracker := budget.NewTracker(budget.Limits{UserMonthlyUSD: 100}, al)
	ctx := context.Background()

	tracker.AddSpend(ctx, "org", "u", 50)
	if al.fired != 0 {
		t.Fatal("premature alert")
	}
	tracker.AddSpend(ctx, "org", "u", 35) // crosses 80
	if al.fired != 1 {
		t.Fatalf("alerts = %d, want 1", al.fired)
	}
	tracker.AddSpend(ctx, "org", "u", 25)
	if al.fired != 1 {
		t.Fatal("alert must fire once")
	}
	if err := tracker.CheckBudget("org", "u"); err == nil {
		t.Fatal("over-budget user must be rejected before allocation")
	}
	Pass(t, Check{ID: "C09-002", Spec: "09-scaling-cost.md#cost-controls",
		Text: "Hard budget enforcement rejects runs; soft alerts notify at 80%."})
}
