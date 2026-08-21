//go:build conformance

package conformance

import "testing"

// Spec: specs/09-scaling-cost.md
func TestScalingCost(t *testing.T) {
	// C09-001 verified by 09_budget_conformance_test.go.
	// C09-002 verified by 09_budget_conformance_test.go.
	t.Run("C09-003_interactive_priority", func(t *testing.T) {
		Pending(t, Check{ID: "C09-003", Spec: "09-scaling-cost.md#noisy-neighbor-protection",
			Text: "Interactive workloads are prioritized over background via separate queues/priority classes."})
	})
}
