// Package conformance maps every normative statement in the specs to an
// automated check. A check is PENDING until the feature it verifies exists
// and the test asserts real behavior; "CI green" then means "complies with
// spec" (docs/testing.md).
//
// Conventions:
//   - Check IDs are stable: C<spec#>-<seq>, e.g. C04-001.
//   - Each check lives in a file named after its spec section.
//   - New checks start as conformance.Pending and flip to real assertions
//     (calling conformance.Pass at the end) when the feature lands.
package conformance

import (
	"fmt"
	"sync"
	"testing"
)

// Check identifies one normative statement from the specs.
type Check struct {
	ID   string `json:"id"`   // stable, e.g. "C04-001"
	Spec string `json:"spec"` // spec file + anchor, e.g. "04-identity-scoping.md#run-token"
	Text string `json:"text"` // the requirement being verified
}

var (
	mu     sync.Mutex
	all    []Check
	passed = map[string]bool{}
)

func register(c Check) {
	mu.Lock()
	defer mu.Unlock()
	for _, existing := range all {
		if existing.ID == c.ID {
			panic(fmt.Sprintf("duplicate conformance check ID %s", c.ID))
		}
	}
	all = append(all, c)
}

// Pending marks a check as specified-but-not-yet-implemented and skips the
// enclosing subtest so it is visible in output without failing CI.
func Pending(t *testing.T, c Check) {
	t.Helper()
	register(c)
	t.Skipf("PENDING %s (%s): %s", c.ID, c.Spec, c.Text)
}

// Pass records that the enclosing test verified the check with real
// assertions. Call as the last step of a passing test.
func Pass(t *testing.T, c Check) {
	t.Helper()
	register(c)
	mu.Lock()
	defer mu.Unlock()
	passed[c.ID] = true
}
