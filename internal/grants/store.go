// Package grants implements the permission ledger (specs/03, specs/04):
// consent requests, grant records with consent evidence, immediate
// revocation, and a short-TTL scope cache for gateways.
package grants

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrAlreadyDecided = errors.New("consent request already decided")
)

// Grant is a user-consented permission record.
type Grant struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	Resource  string     `json:"resource"`
	Scope     string     `json:"scope"`
	Expiry    *time.Time `json:"expiry,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ConsentRequest tracks a pending scope request awaiting user approval.
type ConsentRequest struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	Resource  string     `json:"resource"`
	Scope     string     `json:"scope"`
	Status    string     `json:"status"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
}

// Store persists grants and consent requests. Every state change writes an
// audit event in the same transaction (specs/10).
type Store struct {
	DB *sql.DB
}

func audit(ctx context.Context, tx *sql.Tx, actor, action, resource, decision, reason string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_events (actor, action, resource, decision, reason)
		 VALUES ($1, $2, $3, $4, $5)`,
		actor, action, resource, decision, reason)
	return err
}

// RequestConsent opens a pending consent request for user approval.
func (s *Store) RequestConsent(ctx context.Context, orgID, userID, resource, scope string) (*ConsentRequest, error) {
	var id string
	err := s.DB.QueryRowContext(ctx,
		`INSERT INTO consent_requests (org_id, user_id, resource, scope)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		orgID, userID, resource, scope).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &ConsentRequest{ID: id, UserID: userID, Resource: resource, Scope: scope, Status: "pending"}, nil
}

// DecideConsent approves or denies a pending request. On approval a grant is
// recorded with full consent evidence; both paths emit audit events.
func (s *Store) DecideConsent(ctx context.Context, orgID, reqID string, approved bool, decidedBy string) (*Grant, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var status, resource, scope string
	var userID string
	err = tx.QueryRowContext(ctx,
		`SELECT status, resource, scope, user_id FROM consent_requests
		 WHERE id=$1 AND org_id=$2 FOR UPDATE`, reqID, orgID).
		Scan(&status, &resource, &scope, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return nil, ErrAlreadyDecided
	}

	newStatus := "denied"
	if approved {
		newStatus = "approved"
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE consent_requests SET status=$1, decided_at=now(), decided_by=$2 WHERE id=$3`,
		newStatus, decidedBy, reqID); err != nil {
		return nil, err
	}

	action := "consent.deny"
	if approved {
		action = "consent.approve"
	}
	if err := audit(ctx, tx, decidedBy, action, resource+":"+scope, newStatus, fmt.Sprintf("request=%s", reqID)); err != nil {
		return nil, err
	}

	if !approved {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil // denied: no grant
	}

	var g Grant
	err = tx.QueryRowContext(ctx,
		`INSERT INTO grants (org_id, user_id, resource, scope, consent_record)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, user_id, resource, scope, expiry, revoked_at, created_at`,
		orgID, userID, resource, scope,
		fmt.Sprintf(`{"requestId":%q,"decidedBy":%q}`, reqID, decidedBy)).
		Scan(&g.ID, &g.UserID, &g.Resource, &g.Scope, &g.Expiry, &g.RevokedAt, &g.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, decidedBy, "grant.create", resource+":"+scope, "allow",
		fmt.Sprintf("grant=%s", g.ID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &g, nil
}

// Revoke sets revoked_at on a grant and emits an audit event.
func (s *Store) Revoke(ctx context.Context, orgID, grantID, actor string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	tag, err := tx.ExecContext(ctx,
		`UPDATE grants SET revoked_at=now()
		 WHERE id=$1 AND org_id=$2 AND revoked_at IS NULL`, grantID, orgID)
	if err != nil {
		return err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := audit(ctx, tx, actor, "grant.revoke", grantID, "deny", "user revocation"); err != nil {
		return err
	}
	return tx.Commit()
}

// ActiveScopes returns all non-revoked, non-expired scopes held by the user.
func (s *Store) ActiveScopes(ctx context.Context, orgID, userID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT DISTINCT scope FROM grants
		 WHERE org_id=$1 AND user_id=$2 AND revoked_at IS NULL
		   AND (expiry IS NULL OR expiry > now())`, orgID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sc string
		if err := rows.Scan(&sc); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}
