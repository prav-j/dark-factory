// Package authn provides OIDC authentication middleware: RS256 JWT
// validation against a remote JWKS, claim extraction, and mapping of the
// external subject to internal org/user identities.
package authn

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoToken      = errors.New("missing bearer token")
	ErrMalformed    = errors.New("malformed token")
	ErrBadSignature = errors.New("token signature invalid")
	ErrExpired      = errors.New("token expired")
	ErrBadIssuer    = errors.New("unexpected issuer")
	ErrUnknownKey   = errors.New("unknown signing key")
	ErrUnknownUser  = errors.New("authenticated subject is not provisioned")
)

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// RealClock reads the wall clock.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// Identity is the resolved caller identity attached to request contexts.
type Identity struct {
	UserID string // internal user UUID
	OrgID  string // internal org UUID
	Sub    string // external auth subject
	Email  string
}

type ctxKey struct{}

// FromContext returns the Identity placed by the middleware.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Resolver maps an authenticated external subject to internal identity UUIDs.
// Unknown subjects are rejected (users must be provisioned first).
type Resolver interface {
	Resolve(ctx context.Context, sub string) (orgID, userID string, err error)
}

// Authenticator validates bearer tokens issued by an OIDC provider and
// attaches a resolved Identity to the request context.
type Authenticator struct {
	Issuer   string
	Resolver Resolver
	Clock    Clock

	mu    sync.Mutex
	keys  map[string]*rsa.PublicKey
	fetch time.Time // last JWKS fetch
}

// NewAuthenticator creates an authenticator for the given issuer.
func NewAuthenticator(issuer string, resolver Resolver) *Authenticator {
	return &Authenticator{
		Issuer:   issuer,
		Resolver: resolver,
		Clock:    RealClock{},
		keys:     map[string]*rsa.PublicKey{},
	}
}

// Middleware wraps next with authentication. Routes mounted behind it reject
// requests without a valid bearer token belonging to a provisioned user.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.authenticate(r)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrUnknownUser) {
				status = http.StatusForbidden
			}
			writeErr(w, status, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func (a *Authenticator) authenticate(r *http.Request) (Identity, error) {
	token := bearerToken(r)
	if token == "" {
		return Identity{}, ErrNoToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrMalformed
	}
	header := parseJWTHeader(parts[0])
	if header.KID == "" || header.Alg != "RS256" {
		return Identity{}, ErrMalformed
	}

	key, err := a.signingKey(header.KID, token)
	if err != nil {
		return Identity{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, ErrMalformed
	}
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return Identity{}, ErrBadSignature
	}

	claims, err := parseClaims(parts[1])
	if err != nil {
		return Identity{}, ErrMalformed
	}
	if claims.Exp != 0 && a.Clock.Now().Unix() >= claims.Exp {
		return Identity{}, ErrExpired
	}
	if claims.Iss != a.Issuer {
		return Identity{}, ErrBadIssuer
	}

	orgID, userID, err := a.Resolver.Resolve(r.Context(), claims.Sub)
	if err != nil {
		return Identity{}, ErrUnknownUser
	}
	return Identity{UserID: userID, OrgID: orgID, Sub: claims.Sub, Email: claims.Email}, nil
}

// signingKey returns the public key for kid, refreshing JWKS once if unknown.
func (a *Authenticator) signingKey(kid, token string) (*rsa.PublicKey, error) {
	a.mu.Lock()
	key, ok := a.keys[kid]
	a.mu.Unlock()
	if ok {
		return key, nil
	}
	if err := a.refreshKeys(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key, ok = a.keys[kid]
	if !ok {
		return nil, ErrUnknownKey
	}
	return key, nil
}

func (a *Authenticator) refreshKeys() error {
	cfgURL := strings.TrimSuffix(a.Issuer, "/") + "/.well-known/openid-configuration"
	var cfg struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := getJSON(cfgURL, &cfg); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	var doc struct {
		Keys []struct {
			KID string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getJSON(cfg.JWKSURI, &doc); err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range doc.Keys {
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		a.keys[k.KID] = pub
	}
	return nil
}

func getJSON(rawURL string, into any) error {
	resp, err := http.DefaultClient.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

type jwtHeader struct {
	Alg string `json:"alg"`
	KID string `json:"kid"`
}

func parseJWTHeader(seg string) jwtHeader {
	var h jwtHeader
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err == nil {
		_ = json.Unmarshal(raw, &h)
	}
	return h
}

type jwtClaims struct {
	Iss   string `json:"iss"`
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Exp   int64  `json:"exp"`
}

func parseClaims(seg string) (jwtClaims, error) {
	var c jwtClaims
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
