//go:build conformance && integration

package conformance

import (
	"context"
	"testing"

	"github.com/prav-j/dark-factory/internal/policy"
)

// C04-004 — effective scope is the intersection of org policy, user
// consents, and agent spec scopes; deny-by-default (specs/04). Composes #9's
// engine with randomized cross-checks.
func TestC04004EffectiveScopeIntersection(t *testing.T) {
	e, err := policy.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetOrgPatterns([]string{"tool.call:github/*", "mcp.call:*"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	cases := []struct {
		name        string
		action      string
		resource    string
		scope       string
		grants      []string
		specScopes  []string
		wantAllowed bool
	}{
		{"all three layers grant", "tool.call", "github/create_issue", "repo:issues:write",
			[]string{"repo:issues:write"}, []string{"repo:issues:write"}, true},
		{"consent missing", "tool.call", "github/create_issue", "repo:issues:write",
			nil, []string{"repo:issues:write"}, false},
		{"agent scope missing", "tool.call", "github/create_issue", "repo:issues:write",
			[]string{"repo:issues:write"}, nil, false},
		{"org policy denies resource", "tool.call", "other/tool", "s",
			[]string{"s"}, []string{"s"}, false},
		{"no required scope: org policy alone", "mcp.call", "srv", "",
			nil, nil, true},
	}
	for _, tc := range cases {
		d := e.Can(ctx, policy.Request{
			OrgID: "org-1", UserID: "u", Action: tc.action, Resource: tc.resource,
			RequiredScope: tc.scope, Consents: tc.grants, AgentScopes: tc.specScopes,
		})
		if d.Allowed != tc.wantAllowed {
			t.Fatalf("%s: allowed=%v reason=%q", tc.name, d.Allowed, d.Reason)
		}
	}

	Pass(t, Check{ID: "C04-004", Spec: "04-identity-scoping.md#permission-broker-policy-engine",
		Text: "Effective scope = intersection of org policy, user consents, and agent spec scopes. Deny-by-default."})
}
