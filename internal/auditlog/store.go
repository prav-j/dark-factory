// Package auditlog queries and verifies the append-only audit log
// (specs/10): filterable reads plus hash-chain tamper detection.
package auditlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Event is one auditable occurrence.
type Event struct {
	ID       int64     `json:"id"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Resource string    `json:"resource"`
	Decision string    `json:"decision"`
	Reason   string    `json:"reason,omitempty"`
	Occurred time.Time `json:"occurredAt"`
	PrevHash string    `json:"prevHash"`
	Hash     string    `json:"entryHash"`
}

// Filter narrows queries; zero-value fields are ignored.
type Filter struct {
	Resource string
	Actor    string
	Action   string
	Decision string
	Limit    int
}

// ErrChainBroken indicates tampering or loss somewhere in the log.
var ErrChainBroken = errors.New("audit chain verification failed")

// Store reads the audit log.
type Store struct {
	DB *sql.DB
}

func (s *Store) query(ctx context.Context, where string, args []any, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT id, actor, action, resource, decision, reason, occurred_at, prev_hash, entry_hash
	      FROM audit_events ` + where + ` ORDER BY id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Resource, &e.Decision,
			&e.Reason, &e.Occurred, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Query returns newest-first events matching the filter.
func (s *Store) Query(ctx context.Context, f Filter) ([]Event, error) {
	var conds []string
	var args []any
	add := func(col, val string) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if f.Resource != "" {
		add("resource", f.Resource)
	}
	if f.Actor != "" {
		add("actor", f.Actor)
	}
	if f.Action != "" {
		add("action", f.Action)
	}
	if f.Decision != "" {
		add("decision", f.Decision)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return s.query(ctx, where, args, f.Limit)
}

// expectedHash recomputes the chain digest for an event.
func expectedHash(prev string, e Event) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%s|%s",
		prev, e.ID, e.Actor, e.Action, e.Resource, e.Decision, e.Reason)))
	return hex.EncodeToString(sum[:])
}

// VerifyChain walks the log oldest-first confirming each entry commits to
// its predecessor and matches its own recomputed hash. Returns the number of
// verified entries or ErrChainBroken with the failing ID.
func (s *Store) VerifyChain(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, actor, action, resource, decision, reason, prev_hash, entry_hash
		 FROM audit_events ORDER BY id ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	prev := ""
	n := 0
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Resource, &e.Decision,
			&e.Reason, &e.PrevHash, &e.Hash); err != nil {
			return n, err
		}
		if e.PrevHash != prev || e.Hash != expectedHash(prev, e) {
			return n, fmt.Errorf("%w at entry %d", ErrChainBroken, e.ID)
		}
		prev = e.Hash
		n++
	}
	return n, rows.Err()
}

// Tamper simulates an attacker rewriting an entry: only a DB superuser can
// bypass the append-only trigger, which is exactly the scenario the chain
// exists to detect.
func (s *Store) Tamper(ctx context.Context, id int64, newDecision string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE audit_events DISABLE TRIGGER audit_events_append_only`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE audit_events SET decision=$1 WHERE id=$2`, newDecision, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE audit_events ENABLE TRIGGER audit_events_append_only`); err != nil {
		return err
	}
	return tx.Commit()
}
