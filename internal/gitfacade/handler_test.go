package gitfacade_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/gitfacade"
	"github.com/prav-j/dark-factory/internal/runtoken"
)

const (
	orgID  = "org-1"
	userID = "user-1"
	runID  = "run-77"
)

type staticSessions struct{ info runtoken.SessionInfo }

func (s staticSessions) GetSession(_ context.Context, _ string) (runtoken.SessionInfo, error) {
	return s.info, nil
}

type noRevocations struct{}

func (noRevocations) Revoke(_ context.Context, _ string, _ time.Duration) error { return nil }
func (noRevocations) IsRevoked(_ context.Context, _ string) (bool, error)       { return false, nil }

// fakeUpstream records the auth header it received and replies OK.
type fakeUpstream struct {
	auth      []string
	lastPath  string
	lastQuery string
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.lastPath = r.URL.Path
	f.lastQuery = r.URL.RawQuery
	body, _ := io.ReadAll(r.Body)
	w.Write(body)
}

type staticCreds struct{ user, pass string }

func (s staticCreds) Fetch(_ context.Context, _, _, _ string) (string, string, error) {
	return s.user, s.pass, nil
}

type recordingPR struct {
	called bool
	head   string
}

func (r *recordingPR) CreatePullRequest(_ context.Context, _, _, _, head, _, _, _ string) (string, error) {
	r.called = true
	r.head = head
	return "https://github.com/acme/api/pull/482", nil
}

func setup(t *testing.T) (*httptest.Server, *fakeUpstream, *recordingPR, string) {
	t.Helper()
	sessions := staticSessions{info: runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)}}
	tokens := runtoken.New([]byte("secret"), sessions, noRevocations{})
	upstream := &fakeUpstream{}
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)

	pr := &recordingPR{}
	codec := gitfacade.NewURLCodec("https://facade.internal", []byte("facade-key"))
	handler := gitfacade.NewHandler(codec, tokens, staticCreds{"x-access-token", "ghp_usercred"}, pr,
		func(string) string { return "cred-1" })

	mux := http.NewServeMux()
	mux.Handle("/git/", http.StripPrefix("/git", handler))
	mux.HandleFunc("/pr", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RunToken, Origin, Head, Base, Title string `json:",omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		url, err := handler.CreatePR(r.Context(), req.RunToken, req.Origin, req.Head, req.Base, req.Title, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(url))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, upstream, pr, up.URL[len("http://"):]
}

func mint(tokens *runtoken.Service, grants ...string) string {
	token, _, _ := tokens.Mint(context.Background(), runtoken.MintRequest{
		RunID: runID, SessionID: "sess", UserID: userID, OrgID: orgID, Grants: grants,
	})
	return token
}

func TestRemoteRewriteRoundTrip(t *testing.T) {
	codec := gitfacade.NewURLCodec("https://facade.internal", []byte("k"))
	facade, err := codec.Rewrite(runID, "https://github.com/acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(facade, "https://facade.internal/") {
		t.Fatalf("facade url = %q", facade)
	}
	parts := strings.SplitN(strings.TrimPrefix(facade, "https://facade.internal/"), "/", 2)
	origin, err := codec.Origin(runID, parts[0], parts[1])
	if err != nil || origin != "https://github.com/acme/api" {
		t.Fatalf("origin=%q err=%v", origin, err)
	}
	// A different run's token must not validate this facade URL.
	if _, err := codec.Origin("other-run", parts[0], parts[1]); err == nil {
		t.Fatal("facade URL must be bound to its run")
	}
}

func TestPushProxiedWithUserCredentialsAndAuditTrail(t *testing.T) {
	srv, upstream, pr, upstreamHost := setup(t)

	tokens := runtoken.New([]byte("secret"), staticSessions{
		info: runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)},
	}, noRevocations{})
	codec := gitfacade.NewURLCodec(srv.URL+"/git", []byte("facade-key"))
	origin := "http://" + upstreamHost + "/acme/api"
	facadeURL, _ := codec.Rewrite(runID, origin)

	// Push to a feature branch with a refs payload.
	payload := "0000old\x00new\x00\x00refs/heads/feature-x report-status\n"
	req, _ := http.NewRequest(http.MethodPost, facadeURL+"/git-receive-pack",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.Header.Set("Authorization", "Bearer "+mint(tokens))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push status %d", resp.StatusCode)
	}
	if len(upstream.auth) != 1 || upstream.auth[0] != basicAuth("x-access-token", "ghp_usercred") {
		t.Fatalf("upstream must see the user's injected creds, got %v", upstream.auth)
	}
	if !strings.HasSuffix(upstream.lastPath, "/acme/api/git-receive-pack") {
		t.Fatalf("upstream path = %q", upstream.lastPath)
	}

	// PR creation through the provider abstraction.
	prBody, _ := json.Marshal(map[string]string{
		"runToken": mint(tokens), "origin": origin, "head": "feature-x",
		"base": "main", "title": "Feature X",
	})
	prResp, err := srv.Client().Post(srv.URL+"/pr", "application/json", strings.NewReader(string(prBody)))
	if err != nil || prResp.StatusCode != http.StatusOK {
		t.Fatalf("create PR: status=%d err=%v", prResp.StatusCode, err)
	}
	if !pr.called || pr.head != "feature-x" {
		t.Fatalf("PR not created as user: called=%v head=%q", pr.called, pr.head)
	}
}

func basicAuth(u, p string) string {
	return "Basic " + base64Encode(u+":"+p)
}

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestProtectedBranchDenied(t *testing.T) {
	srv, upstream, _, upstreamHost := setup(t)
	tokens := runtoken.New([]byte("secret"), staticSessions{
		info: runtoken.SessionInfo{Alive: true, Deadline: time.Now().Add(time.Hour)},
	}, noRevocations{})
	codec := gitfacade.NewURLCodec(srv.URL+"/git", []byte("facade-key"))
	facadeURL, _ := codec.Rewrite(runID, "http://"+upstreamHost+"/acme/api")

	payload := "0000old\x00new\x00\x00refs/heads/main report-status\n"
	req, _ := http.NewRequest(http.MethodPost, facadeURL+"/git-receive-pack",
		strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+mint(tokens)) // no bypass grant
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("protected push: %d, want 403", resp.StatusCode)
	}
	if len(upstream.auth) != 0 {
		t.Fatal("denied push must never reach upstream")
	}
}

func TestUnauthenticatedRejected(t *testing.T) {
	srv, upstream, _, _ := setup(t)
	codec := gitfacade.NewURLCodec(srv.URL+"/git", []byte("facade-key"))
	facadeURL, _ := codec.Rewrite(runID, "https://github.com/acme/api")

	resp, err := srv.Client().Get(facadeURL + "/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
	if len(upstream.auth) != 0 {
		t.Fatal("unauthenticated request reached upstream")
	}
}
