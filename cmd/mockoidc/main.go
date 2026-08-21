// Command mockoidc is a development-only OIDC provider for local stacks.
// It generates an RSA keypair at startup and serves:
//
//	GET /.well-known/openid-configuration
//	GET /jwks
//	GET /token?user=&org=&email=   -> {"access_token", "id_token"}
//
// Tokens are RS256 JWTs with org_id/subject claims, valid 1h.
// NEVER use in production.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"math/big"
	"net/http"
	"time"
)

type server struct {
	key *rsa.PrivateKey
	kid string
	iss string
}

func main() {
	addr := flag.String("addr", ":8082", "listen address")
	public := flag.String("public-url", "", "external issuer URL (default: http://localhost+addr)")
	flag.Parse()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	iss := *public
	if iss == "" {
		iss = "http://localhost" + *addr
	}
	s := &server{key: key, kid: "dev-key-1", iss: iss}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("/jwks", s.jwks)
	mux.HandleFunc("/token", s.token)

	log.Printf("mockoidc issuer %s listening on %s", iss, *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func (s *server) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{
		"issuer":   s.iss,
		"jwks_uri": s.iss + "/jwks",
	})
}

func (s *server) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := &s.key.PublicKey
	writeJSON(w, map[string]interface{}{
		"keys": []map[string]string{{
			"kty": "RSA",
			"kid": s.kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}

func (s *server) token(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if user == "" {
		http.Error(w, "missing user", http.StatusBadRequest)
		return
	}
	org := r.URL.Query().Get("org")
	if org == "" {
		org = "org-dev"
	}
	email := r.URL.Query().Get("email")
	if email == "" {
		email = user + "@dev.local"
	}

	now := time.Now()
	header := b64(`{"alg":"RS256","typ":"JWT","kid":"` + s.kid + `"}`)
	payload, _ := json.Marshal(map[string]interface{}{
		"iss":    s.iss,
		"sub":    user,
		"org_id": org,
		"email":  email,
		"iat":    now.Unix(),
		"exp":    now.Add(time.Hour).Unix(),
	})
	signingInput := header + "." + b64(string(payload))
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		http.Error(w, "sign error", http.StatusInternalServerError)
		return
	}
	tok := signingInput + "." + b64(string(sig))

	writeJSON(w, map[string]string{
		"access_token": tok,
		"id_token":     tok,
		"token_type":   "Bearer",
		"expires_in":   "3600",
	})
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
