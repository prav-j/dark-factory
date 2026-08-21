// Package credexchange implements just-in-time credential injection
// (specs/08, specs/13): gateways and git credential-helpers exchange a valid
// Run Token + credential reference for short-lived downstream credentials.
// Plaintext secrets never leave the exchange boundary except to the caller
// that presented a live run token authorized for them.
package credexchange

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prav-j/dark-factory/internal/policy"
	"github.com/prav-j/dark-factory/internal/runtoken"
	"github.com/prav-j/dark-factory/internal/secrets"
)

var (
	ErrUnauthorized = errors.New("run token not authorized for this credential")
	ErrNotFound     = errors.New("credential not found")
)

// Exchanged is the short-lived downstream credential handed to the caller.
type Exchanged struct {
	CredentialRef string    `json:"credentialRef"`
	Username      string    `json:"username"`
	Secret        string    `json:"secret"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// SecretTTL bounds how long an exchanged credential remains usable. Real
// providers would mint scoped, expiring tokens via token exchange; v1 caps
// exposure time contractually.
const SecretTTL = 5 * time.Minute

// Service validates run tokens and policy before releasing credentials.
type Service struct {
	Tokens *runtoken.Service
	Secret *secrets.Manager
	Policy *policy.Engine
}

// Exchange returns the plaintext credential for credentialRef if the run
// token is valid and org policy permits access. The audit trail records the
// release against the run id.
func (s *Service) Exchange(ctx context.Context, runToken, credentialRef string) (*Exchanged, error) {
	claims, err := s.Tokens.Validate(ctx, runToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}

	d := s.Policy.Can(ctx, policy.Request{
		OrgID:         claims.OrgID,
		UserID:        claims.UserID,
		Action:        "credential.read",
		Resource:      credentialRef,
		RequiredScope: "credentials:read",
		// Credential access is governed by org policy + user consent; the
		// run token's grants are both the consent and the declaration here.
		Consents:    claims.Grants,
		AgentScopes: claims.Grants,
	})
	if !d.Allowed {
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, d.Reason)
	}

	pt, err := s.Secret.Get(ctx, claims.OrgID, claims.UserID, credentialRef)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) || errors.Is(err, secrets.ErrWrongTenant) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &Exchanged{
		CredentialRef: credentialRef,
		Username:      "x-access-token", // git convention; callers may override per provider
		Secret:        string(pt),
		ExpiresAt:     time.Now().Add(SecretTTL),
	}, nil
}
