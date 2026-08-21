package testutil

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// FakeOIDC is an in-process OIDC provider. It mints signed (RS256) ID tokens
// for arbitrary user/org identities and serves standard discovery + JWKS
// endpoints so authn middleware can validate them.
type FakeOIDC struct {
	srv *httptest.Server
	url string

	mu     sync.Mutex
	key    *rsa.PrivateKey
	kid    string
	nextID int64
	issued map[string]oidcClaims
}

type oidcClaims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	OrgID    string `json:"org_id"`
	Email    string `json:"email"`
	Expiry   int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
}

// NewFakeOIDC starts the provider and registers cleanup.
func NewFakeOIDC(t *testing.T) *FakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	f := &FakeOIDC{
		key:    key,
		kid:    "test-key-1",
		issued: map[string]oidcClaims{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/jwks", f.jwks)
	f.srv = httptest.NewServer(mux)
	f.url = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the issuer base address.
func (f *FakeOIDC) URL() string { return f.url }

// MintToken returns a signed ID token for the given identity.
func (f *FakeOIDC) MintToken(userID, orgID, email string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	now := time.Now()
	claims := oidcClaims{
		Issuer:   f.url,
		Subject:  userID,
		OrgID:    orgID,
		Email:    email,
		IssuedAt: now.Unix(),
		Expiry:   now.Add(time.Hour).Unix(),
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"` + f.kid + `"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return ""
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := header + "." + body

	sum, err := signRS256(f.key, signingInput)
	if err != nil {
		return ""
	}
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sum)
	f.issued[token] = claims
	return token
}

// Claims decodes and returns the claims for an issued token.
func (f *FakeOIDC) Claims(token string) (oidcClaims, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.issued[token]
	return c, ok
}

// Verify checks a token's signature against the provider key.
func (f *FakeOIDC) Verify(token string) error {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return errMalformedToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errMalformedToken
	}
	return rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sha256sum(parts[0]+"."+parts[1]), sig)
}

func (f *FakeOIDC) discovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"issuer":   f.url,
		"jwks_uri": f.url + "/jwks",
	})
}

func (f *FakeOIDC) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := &f.key.PublicKey
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"keys": []map[string]string{{
			"kty": "RSA",
			"kid": f.kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}
