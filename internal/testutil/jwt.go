package testutil

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"strings"
)

var errMalformedToken = errors.New("malformed JWT")

func splitJWT(token string) []string {
	return strings.Split(token, ".")
}

func sha256sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func signRS256(key *rsa.PrivateKey, signingInput string) ([]byte, error) {
	return rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sha256sum(signingInput))
}
