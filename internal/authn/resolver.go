package authn

import (
	"context"
	"database/sql"
	"errors"
)

// DBResolver resolves external subjects to internal identities via the
// users table (auth_subject is unique — see migration 001).
type DBResolver struct {
	DB *sql.DB
}

// Resolve returns (orgID, userID) for a provisioned subject.
func (r *DBResolver) Resolve(ctx context.Context, sub string) (orgID, userID string, err error) {
	if r == nil || r.DB == nil {
		return "", "", errors.New("authn: no resolver backing store")
	}
	err = r.DB.QueryRowContext(ctx,
		`SELECT org_id, id FROM users WHERE auth_subject = $1`, sub).Scan(&orgID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUnknownUser
	}
	return orgID, userID, err
}
