package policy_test

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/prav-j/dark-factory/internal/policy"
)

func mustEngine(t *testing.T, patterns ...string) *policy.Engine {
	t.Helper()
	e, err := policy.NewEngine()
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	if err := e.SetOrgPatterns(patterns); err != nil {
		t.Fatalf("set patterns: %v", err)
	}
	return e
}

func TestDenyByDefault(t *testing.T) {
	e := mustEngine(t) // no patterns
	d := e.Can(context.Background(), policy.Request{
		OrgID: "org-1", UserID: "user-1",
		Action: "tool.call", Resource: "builtin/web_search",
	})
	if d.Allowed {
		t.Fatal("empty org policies must deny everything")
	}
}

func TestOrgPatternMatching(t *testing.T) {
	e := mustEngine(t, "tool.call:builtin/*", "mcp.call:github-official")
	ctx := context.Background()

	cases := []struct {
		action, resource string
		want             bool
	}{
		{"tool.call", "builtin/web_search", true},
		{"tool.call", "builtin/code_interpreter", true},
		{"tool.call", "github/create_issue", false}, // not builtin/*
		{"mcp.call", "github-official", true},
		{"mcp.call", "other-server", false},
		{"admin.grant", "anything", false},
	}
	for _, tc := range cases {
		d := e.Can(ctx, policy.Request{OrgID: "org-1", UserID: "u", Action: tc.action, Resource: tc.resource})
		if d.Allowed != tc.want {
			t.Errorf("%s:%s allowed=%v, want %v (%s)", tc.action, tc.resource, d.Allowed, tc.want, d.Reason)
		}
	}
}

func TestEffectiveScopeIntersection(t *testing.T) {
	e := mustEngine(t, "tool.call:github/create_issue")
	ctx := context.Background()
	base := policy.Request{
		OrgID: "org-1", UserID: "u",
		Action:        "tool.call",
		Resource:      "github/create_issue",
		RequiredScope: "repo:issues:write",
	}

	// Scope in consents but not agent spec -> deny.
	d := e.Can(ctx, withScopes(base, []string{"repo:issues:write"}, nil))
	if d.Allowed {
		t.Fatal("consent without agent-spec scope must deny")
	}

	// Scope in agent spec but no consent -> deny.
	d = e.Can(ctx, withScopes(base, nil, []string{"repo:issues:write"}))
	if d.Allowed {
		t.Fatal("agent-spec scope without consent must deny")
	}

	// Both -> allow.
	d = e.Can(ctx, withScopes(base, []string{"repo:issues:write"}, []string{"repo:issues:write"}))
	if !d.Allowed {
		t.Fatalf("full intersection must allow: %s", d.Reason)
	}

	// Org policy denies the resource entirely -> deny regardless of scopes.
	e2 := mustEngine(t) // nothing allowed
	d = e2.Can(ctx, withScopes(base, []string{"repo:issues:write"}, []string{"repo:issues:write"}))
	if d.Allowed {
		t.Fatal("org policy denial overrides scope intersection")
	}
}

// TestPropertyRandomized compares the engine against a naive reference
// implementation across randomly generated inputs (fixed seed).
func TestPropertyRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic tests
	scopes := []string{"s1", "s2", "s3"}
	patternPool := []string{"tool.call:a/*", "tool.call:b", "mcp.call:*"}

	for i := 0; i < 200; i++ {
		var patterns []string
		for _, p := range patternPool {
			if rng.Intn(2) == 1 {
				patterns = append(patterns, p)
			}
		}
		e := mustEngine(t, patterns...)

		action := []string{"tool.call", "tool.call", "mcp.call", "unknown.action"}[rng.Intn(4)]
		resource := []string{"a/x", "b", "server-1", "c/y"}[rng.Intn(4)]
		scope := scopes[rng.Intn(len(scopes))]

		var consents, agentScopes []string
		if rng.Intn(2) == 1 {
			consents = append(consents, scope)
		}
		if rng.Intn(2) == 1 {
			agentScopes = append(agentScopes, scope)
		}

		req := policy.Request{
			OrgID: "org", UserID: "u", Action: action, Resource: resource,
			RequiredScope: scope, Consents: consents, AgentScopes: agentScopes,
		}
		got := e.Can(context.Background(), req)

		orgOK := naiveGlobAllowed(patterns, action+":"+resource)
		want := orgOK && contains(consents, scope) && contains(agentScopes, scope)
		if got.Allowed != want {
			t.Fatalf("case %d: engine=%v want=%v (patterns=%v action=%s resource=%s)",
				i, got.Allowed, want, patterns, action, resource)
		}
	}
}

func withScopes(r policy.Request, consents, agentScopes []string) policy.Request {
	r.Consents = consents
	r.AgentScopes = agentScopes
	return r
}

func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

// naiveGlobAllowed mirrors glob.match with ":" delimiter for the test's
// fixed pattern shapes ("prefix*" star-suffix and exact matches).
func naiveGlobAllowed(patterns []string, candidate string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(candidate, p[:len(p)-1]) {
				return true
			}
			continue
		}
		if p == candidate {
			return true
		}
	}
	return false
}
