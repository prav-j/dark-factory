package gitfacade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/prav-j/dark-factory/internal/audit"
	"github.com/prav-j/dark-factory/internal/runtoken"
)

// AuditDB is optional; when set, every mutation is recorded to the
// append-only audit log.
func (h *Handler) writeAudit(ctx context.Context, actor, action, resource, decision, reason string) {
	if h.auditDB == nil {
		return
	}
	_ = audit.Write(ctx, h.auditDB, actor, action, resource, decision, reason)
}

// CredentialSource fetches the user's upstream credentials just-in-time.
type CredentialSource interface {
	Fetch(ctx context.Context, orgID, userID, credentialRef string) (username, secret string, err error)
}

// PRCreator opens pull requests against the provider API as the user.
type PRCreator interface {
	CreatePullRequest(ctx context.Context, orgID, userID, origin, head, base, title, body string) (prURL string, err error)
}

// Handler serves the git HTTP protocol endpoints proxied to the origin:
//
//	/{token}/{encodedOrigin}/info/refs?service=git-upload-pack   (fetch/clone: allowed)
//	/{token}/{encodedOrigin}/git-receive-pack                    (push: policy-checked + audited)
//
// Authenticated via the session's run token in the Authorization header —
// the same token the sandbox already holds; raw provider creds are injected
// only on the facade->upstream hop.
type Handler struct {
	codec      *URLCodec
	tokens     *runtoken.Service
	creds      CredentialSource
	prs        PRCreator
	upstream   *http.Client
	credRefFor func(origin string) string
	auditDB    *sql.DB
}

func NewHandler(codec *URLCodec, tokens *runtoken.Service, creds CredentialSource,
	prs PRCreator, credRefFor func(string) string) *Handler {
	return &Handler{
		codec: codec, tokens: tokens, creds: creds, prs: prs,
		upstream: &http.Client{}, credRefFor: credRefFor,
	}
}

// WithAuditDB enables append-only audit logging of git mutations.
func (h *Handler) WithAuditDB(db *sql.DB) *Handler { h.auditDB = db; return h }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := h.tokens.Validate(r.Context(), bearer(r))
	if err != nil {
		unauthorized(w)
		return
	}

	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	token, encoded := parts[0], parts[1]
	origin, err := h.codec.Origin(claims.RunID, token, encoded)
	if err != nil {
		unauthorized(w)
		return
	}
	remainder := ""
	if len(parts) == 3 {
		remainder = parts[2]
	}

	isPush := strings.Contains(remainder, "git-receive-pack")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))

	if isPush {
		if d := CheckPush(claims.Grants, BranchesFromPayload(body)); !d.Allowed {
			h.writeAudit(r.Context(), claims.UserID, "git.push", origin, "deny", d.Reason)
			http.Error(w, d.Reason, http.StatusForbidden)
			return
		}
	}

	username, secret, err := h.creds.Fetch(r.Context(), claims.OrgID, claims.UserID, h.credRefFor(origin))
	if err != nil {
		http.Error(w, "credential unavailable", http.StatusForbidden)
		return
	}

	upURL := origin
	if !strings.HasPrefix(upURL, "http") {
		upURL = "https://" + upURL
	}
	if remainder != "" {
		upURL = strings.TrimSuffix(upURL, "/") + "/" + remainder
	}
	if r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upURL, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "proxy error", http.StatusInternalServerError)
		return
	}
	req.SetBasicAuth(username, secret)
	for _, hdr := range []string{"Content-Type", "Accept", "User-Agent"} {
		if v := r.Header.Get(hdr); v != "" {
			req.Header.Set(hdr, v)
		}
	}

	resp, err := h.upstream.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if isPush && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		h.writeAudit(r.Context(), claims.UserID, "git.push", origin, "allow",
			fmt.Sprintf("branches=%v", BranchesFromPayload(body)))
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// CreatePR proxies pull-request creation through the provider API as the user.
func (h *Handler) CreatePR(ctx context.Context, runToken, origin, head, base, title, body string) (string, error) {
	claims, err := h.tokens.Validate(ctx, runToken)
	if err != nil {
		return "", fmt.Errorf("unauthorized: %w", err)
	}
	if h.prs == nil {
		return "", errors.New("no provider configured")
	}
	url, err := h.prs.CreatePullRequest(ctx, claims.OrgID, claims.UserID, origin, head, base, title, body)
	if err != nil {
		return "", err
	}
	h.writeAudit(ctx, claims.UserID, "git.pr.create", origin+":"+head, "allow", "url="+url)
	return url, nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="dark-factory-git"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
