// Package audit writes append-only audit events (specs/10). Every
// authorization-relevant state change must produce an event, ideally in the
// same transaction as the change itself.
package audit

import (
	"context"
	"database/sql"
)

// Executor is anything that can run a query: *sql.DB or *sql.Tx.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Write records one audit event.
func Write(ctx context.Context, ex Executor, actor, action, resource, decision, reason string) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO audit_events (actor, action, resource, decision, reason)
		 VALUES ($1, $2, $3, $4, $5)`,
		actor, action, resource, decision, reason)
	return err
}
