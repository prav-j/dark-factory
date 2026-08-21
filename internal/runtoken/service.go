// Package runtoken mints and validates the short-lived Run Tokens that gate
// all tool/MCP egress (specs/04-identity-scoping.md). Tokens are bound to a
// run + session, expire in 15 minutes, are renewable only while the parent
// session lives (never past its deadline), and can be revoked by jti.
package runtoken

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prav-j/dark-factory/internal/testutil"
)

const defaultTTL = 15 * time.Minute

var (
	ErrMalformed      = errors.New("malformed token")
	ErrBadSignature   = errors.New("token signature invalid")
	ErrExpired        = errors.New("token expired")
	ErrRevoked        = errors.New("token revoked")
	ErrSessionEnded   = errors.New("session not alive")
	ErrSessionExpired = errors.New("session lifetime exceeded")
)

// Claims is the Run Token payload.
type Claims struct {
	RunID     string   `json:"sub"`
	SessionID string   `json:"session"`
	Agent     string   `json:"agent"`
	UserID    string   `json:"acting_as"`
	OrgID     string   `json:"org"`
	Grants    []string `json:"grants,omitempty"`
	MCPServer []string `json:"mcp_servers,omitempty"`
	JTI       string   `json:"jti"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
}

// SessionInfo reports whether a parent session may still renew tokens.
type SessionInfo struct {
	Alive    bool
	Deadline time.Time // hard maxLifetime of the session
}

// SessionChecker is the harness-side liveness source (DDB-backed from M7).
type SessionChecker interface {
	GetSession(ctx context.Context, sessionID string) (SessionInfo, error)
}

// Revocations tracks revoked jtis until their natural expiry.
type Revocations interface {
	Revoke(ctx context.Context, jti string, ttl time.Duration) error
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

// Service mints and validates Run Tokens.
type Service struct {
	secret  []byte
	ttl     time.Duration
	clock   testutil.Clock
	session SessionChecker
	revoke  Revocations
}

func New(secret []byte, session SessionChecker, revocations Revocations) *Service {
	return &Service{
		secret:  secret,
		ttl:     defaultTTL,
		clock:   testutil.RealClock{},
		session: session,
		revoke:  revocations,
	}
}

// SetClock overrides time for tests.
func (s *Service) SetClock(c testutil.Clock) { s.clock = c }

// MintRequest describes a new run.
type MintRequest struct {
	RunID     string
	SessionID string
	Agent     string
	UserID    string
	OrgID     string
	Grants    []string
	MCPServer []string
}

// Mint issues a token valid for TTL, capped at the session deadline.
func (s *Service) Mint(ctx context.Context, req MintRequest) (string, Claims, error) {
	info, err := s.session.GetSession(ctx, req.SessionID)
	if err != nil {
		return "", Claims{}, err
	}
	if !info.Alive {
		return "", Claims{}, ErrSessionEnded
	}
	return s.mintWithDeadline(req, info.Deadline)
}

func (s *Service) mintWithDeadline(req MintRequest, sessionDeadline time.Time) (string, Claims, error) {
	return s.encodeToken(req, sessionDeadline, newJTI())
}

func (s *Service) encodeToken(req MintRequest, sessionDeadline time.Time, jti string) (string, Claims, error) {
	now := s.clock.Now()
	exp := now.Add(s.ttl)
	if !sessionDeadline.IsZero() && exp.After(sessionDeadline) {
		exp = sessionDeadline // never outlive the session
	}
	if !exp.After(now) {
		return "", Claims{}, ErrSessionExpired
	}

	claims := Claims{
		RunID: req.RunID, SessionID: req.SessionID, Agent: req.Agent,
		UserID: req.UserID, OrgID: req.OrgID,
		Grants: req.Grants, MCPServer: req.MCPServer,
		JTI: jti, IssuedAt: now.Unix(), ExpiresAt: exp.Unix(),
	}
	return encode(claims, s.secret), claims, nil
}

// Renew extends a still-valid token within its live session. The renewed
// token keeps the same jti so any prior revocation survives renewal.
func (s *Service) Renew(ctx context.Context, token string) (string, Claims, error) {
	claims, err := s.Validate(ctx, token)
	if err != nil {
		return "", Claims{}, err
	}
	info, err := s.session.GetSession(ctx, claims.SessionID)
	if err != nil {
		return "", Claims{}, err
	}
	if !info.Alive {
		return "", Claims{}, ErrSessionEnded
	}
	req := MintRequest{
		RunID: claims.RunID, SessionID: claims.SessionID, Agent: claims.Agent,
		UserID: claims.UserID, OrgID: claims.OrgID,
		Grants: claims.Grants, MCPServer: claims.MCPServer,
	}
	return s.encodeToken(req, info.Deadline, claims.JTI)
}

// Validate verifies signature, expiry, and revocation. Gateways call this on
// every request.
func (s *Service) Validate(ctx context.Context, token string) (Claims, error) {
	var claims Claims
	if err := decode(token, &claims, s.secret); err != nil {
		return Claims{}, err
	}
	if s.clock.Now().Unix() >= claims.ExpiresAt {
		return Claims{}, ErrExpired
	}
	if s.revoke != nil {
		revoked, err := s.revoke.IsRevoked(ctx, claims.JTI)
		if err != nil {
			return Claims{}, err
		}
		if revoked {
			return Claims{}, ErrRevoked
		}
	}
	return claims, nil
}

// Revoke invalidates a token by jti for the remainder of its validity.
func (s *Service) Revoke(ctx context.Context, token string) error {
	var claims Claims
	if err := decode(token, &claims, s.secret); err != nil {
		return err
	}
	ttl := time.Duration(claims.ExpiresAt-s.clock.Now().Unix()) * time.Second
	if ttl <= 0 {
		return nil // already expired; nothing to revoke
	}
	return s.revoke.Revoke(ctx, claims.JTI, ttl)
}

// --- encoding (compact JWS-style, HS256) ---

func encode(claims Claims, secret []byte) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(header + "." + body))
	return header + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decode(token string, into *Claims, secret []byte) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ErrMalformed
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(want, got) {
		return ErrBadSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrMalformed
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return ErrMalformed
	}
	return nil
}

func newJTI() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("jti entropy: %v", err)) // crypto/rand failure is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
