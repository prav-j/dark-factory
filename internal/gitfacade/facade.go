// Package gitfacade proxies in-session git operations (specs/06, specs/15):
// sandbox remotes are rewritten to point at the facade, which authenticates
// with the *user's* credentials fetched just-in-time, enforces policy
// (protected branches), audits every mutation, and never exposes raw
// credentials inside the sandbox.
package gitfacade

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrBadFacadeURL = errors.New("malformed facade URL")

// FacadeURLs builds/parses the rewritten remote URLs sandboxes see:
//
//	https://<facade>/<token>/<b64(origin)>/...   token = HMAC(origin+run)
type URLCodec struct {
	facadeBase string
	signingKey []byte
}

func NewURLCodec(facadeBase string, signingKey []byte) *URLCodec {
	return &URLCodec{
		facadeBase: strings.TrimSuffix(facadeBase, "/"),
		signingKey: signingKey,
	}
}

// Rewrite maps an origin URL onto a facade URL bound to one run.
func (c *URLCodec) Rewrite(runID, origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrBadFacadeURL, origin)
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(origin))
	return fmt.Sprintf("%s/%s/%s", c.facadeBase, c.sign(runID, origin), enc), nil
}

// Origin extracts the original remote from a facade URL path segment,
// verifying the per-run signature.
func (c *URLCodec) Origin(runID, token, encoded string) (string, error) {
	if !hmac.Equal([]byte(token), []byte(c.sign(runID, decodeB64(encoded)))) {
		return "", fmt.Errorf("%w: bad token", ErrBadFacadeURL)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadFacadeURL, err)
	}
	return string(raw), nil
}

func (c *URLCodec) sign(runID, origin string) string {
	mac := hmac.New(sha256.New, c.signingKey)
	mac.Write([]byte(runID))
	mac.Write([]byte{0})
	mac.Write([]byte(origin))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

func decodeB64(s string) string {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return s // verification will fail downstream
	}
	return string(raw)
}

// ProtectedBranches is the default deny-push list; org policy extends it.
var ProtectedBranches = map[string]bool{"main": true, "master": true, "release": true}

// BranchFromRefsHeader inspects the raw refs the client wants to update.
// Git sends old-ref\0new-ref\0... in the receive-pack body; we only need the
// ref name for policy, so a lightweight scan suffices.
func BranchesFromPayload(body []byte) []string {
	var out []string
	for _, part := range strings.Split(string(body), "\x00") {
		if idx := strings.Index(part, "refs/heads/"); idx >= 0 {
			end := idx + len("refs/heads/")
			for end < len(part) && part[end] != ' ' && part[end] != '\n' {
				end++
			}
			out = append(out, part[idx+len("refs/heads/"):end])
		}
	}
	return out
}

// PushDecision is the policy outcome for a push attempt.
type PushDecision struct {
	Allowed bool
	Reason  string
}

// CheckPush denies writes to protected branches unless an explicit bypass
// scope is present (org-configurable).
func CheckPush(grants []string, branches []string) PushDecision {
	const bypass = "git:push:protected"
	for _, b := range branches {
		if ProtectedBranches[b] && !contains(grants, bypass) {
			return PushDecision{false, fmt.Sprintf("branch %q is protected", b)}
		}
	}
	return PushDecision{true, ""}
}

func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}
