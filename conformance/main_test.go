//go:build conformance

package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// TestMain writes conformance-report.json after all checks run, giving CI a
// machine-readable drift signal.
func TestMain(m *testing.M) {
	code := m.Run()

	mu.Lock()
	sum := &report{Total: len(all), PassedI: []string{}}
	for _, c := range all {
		if passed[c.ID] {
			sum.Passed++
			sum.PassedI = append(sum.PassedI, c.ID)
		} else {
			sum.Pending++
		}
	}
	sum.Checks = append([]Check{}, all...)
	mu.Unlock()

	if out := os.Getenv("CONFORMANCE_REPORT"); out != "" {
		if b, err := json.MarshalIndent(sum, "", "  "); err == nil {
			_ = os.WriteFile(out, b, 0o644)
		}
	}
	fmt.Printf("conformance summary: total=%d passed=%d pending=%d\n", sum.Total, sum.Passed, sum.Pending)

	os.Exit(code)
}
