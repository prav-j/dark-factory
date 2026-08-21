// Package registry implements the Agent Registry control-plane service:
// agent/version CRUD with canonicalized spec storage and content hashes
// (specs/03-data-model.md). Lifecycle rules are enforced both here and by a
// DB trigger (defense in depth).
package registry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/prav-j/dark-factory/internal/agentspec"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrImmutable     = errors.New("version is immutable")
	ErrInvalidState  = errors.New("invalid version state transition")
	ErrDuplicateName = errors.New("agent name already exists in org")
)

// Version is one immutable-or-draft revision of an agent spec.
type Version struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agentId"`
	Version     int        `json:"version"`
	Status      string     `json:"status"` // draft | published | deprecated
	SpecHash    string     `json:"specHash"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// Agent is a registered agent pointing at its current published version.
type Agent struct {
	ID                string  `json:"id"`
	OrgID             string  `json:"orgId"`
	OwnerUserID       string  `json:"ownerUserId"`
	Name              string  `json:"name"`
	CurrentVersion    *int    `json:"currentVersion,omitempty"`
	CurrentVersionRef *string `json:"-"`
}

// Store persists agents and versions in Postgres.
type Store struct {
	db *sql.DB
}

// NewStore wraps an open database handle.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Raw exposes the underlying handle for tests and admin tooling.
func (s *Store) Raw() *sql.DB { return s.db }

func hashSpec(canon []byte) string {
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// CreateAgent parses+validates the spec, creates the agent and its first
// draft version, returning both.
func (s *Store) CreateAgent(ctx context.Context, orgID, ownerUserID, name string, specYAML []byte) (*Agent, *Version, error) {
	doc, err := agentspec.Parse(specYAML)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	canon, err := doc.CanonicalJSON()
	if err != nil {
		return nil, nil, err
	}
	hash := hashSpec(canon)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var agentID string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO agents (org_id, owner_user_id, name) VALUES ($1, $2, $3) RETURNING id`,
		orgID, ownerUserID, name).Scan(&agentID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, nil, ErrDuplicateName
		}
		return nil, nil, err
	}

	v, err := insertVersion(ctx, tx, agentID, orgID, 1, canon, hash)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return &Agent{ID: agentID, OrgID: orgID, OwnerUserID: ownerUserID, Name: name}, v, nil
}

// AddVersion appends the next draft version for an existing agent.
func (s *Store) AddVersion(ctx context.Context, orgID, agentID string, specYAML []byte) (*Version, error) {
	doc, err := agentspec.Parse(specYAML)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	canon, err := doc.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	hash := hashSpec(canon)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	err = tx.QueryRowContext(ctx,
		`SELECT true FROM agents WHERE id=$1 AND org_id=$2`, agentID, orgID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var next int
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM agent_versions WHERE agent_id=$1`, agentID).Scan(&next)
	if err != nil {
		return nil, err
	}

	v, err := insertVersion(ctx, tx, agentID, orgID, next, canon, hash)
	if err != nil {
		return nil, err
	}
	return v, tx.Commit()
}

// UpdateDraft replaces the spec of a draft version. Published versions are
// rejected here; the DB trigger is the backstop.
func (s *Store) UpdateDraft(ctx context.Context, orgID, agentID string, version int, specYAML []byte) (*Version, error) {
	doc, err := agentspec.Parse(specYAML)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	canon, err := doc.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	hash := hashSpec(canon)

	tag, err := s.db.ExecContext(ctx,
		`UPDATE agent_versions SET spec=$1, spec_hash=$2
		 WHERE agent_id=$3 AND org_id=$4 AND version=$5 AND status='draft'`,
		canon, hash, agentID, orgID, version)
	if err != nil {
		return nil, err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		if _, err := s.GetVersion(ctx, orgID, agentID, version); err != nil {
			return nil, err
		}
		return nil, ErrImmutable // exists but not a draft
	}
	return s.GetVersion(ctx, orgID, agentID, version)
}

// PublishVersion transitions draft -> published and makes it the agent's
// current version.
func (s *Store) PublishVersion(ctx context.Context, orgID, agentID string, version int) (*Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var id, status string
	err = tx.QueryRowContext(ctx,
		`SELECT id, status FROM agent_versions WHERE agent_id=$1 AND org_id=$2 AND version=$3 FOR UPDATE`,
		agentID, orgID, version).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "draft" {
		return nil, ErrInvalidState
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_versions SET status='published' WHERE id=$1`, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET current_version_id=$1 WHERE id=$2`, id, agentID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetVersionByNumber(ctx, orgID, agentID, version)
}

// DeprecateVersion marks a published version deprecated (the only allowed
// post-publish change).
func (s *Store) DeprecateVersion(ctx context.Context, orgID, agentID string, version int) (*Version, error) {
	tag, err := s.db.ExecContext(ctx,
		`UPDATE agent_versions SET status='deprecated'
		 WHERE agent_id=$1 AND org_id=$2 AND version=$3 AND status='published'`,
		agentID, orgID, version)
	if err != nil {
		return nil, err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		if _, err := s.GetVersion(ctx, orgID, agentID, version); err != nil {
			return nil, err
		}
		return nil, ErrInvalidState
	}
	return s.GetVersionByNumber(ctx, orgID, agentID, version)
}

// GetAgent returns the agent if it belongs to orgID.
func (s *Store) GetAgent(ctx context.Context, orgID, agentID string) (*Agent, error) {
	var a Agent
	var cur sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.org_id, a.owner_user_id, a.name, av.version
		 FROM agents a
		 LEFT JOIN agent_versions av ON av.id = a.current_version_id
		 WHERE a.id=$1 AND a.org_id=$2`, agentID, orgID).
		Scan(&a.ID, &a.OrgID, &a.OwnerUserID, &a.Name, &cur)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.Valid {
		v := int(cur.Int64)
		a.CurrentVersion = &v
	}
	return &a, nil
}

// GetVersion returns a specific version by number.
func (s *Store) GetVersion(ctx context.Context, orgID, agentID string, version int) (*Version, error) {
	return queryVersion(s, ctx,
		`SELECT id, agent_id, version, status, spec_hash, published_at, created_at
		 FROM agent_versions WHERE agent_id=$1 AND org_id=$2 AND version=$3`,
		agentID, orgID, version)
}

// GetVersionByNumber is an alias kept for call-site readability after publish.
func (s *Store) GetVersionByNumber(ctx context.Context, orgID, agentID string, version int) (*Version, error) {
	return s.GetVersion(ctx, orgID, agentID, version)
}

// ListVersions returns all versions of an agent, newest first.
func (s *Store) ListVersions(ctx context.Context, orgID, agentID string) ([]*Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, version, status, spec_hash, published_at, created_at
		 FROM agent_versions WHERE agent_id=$1 AND org_id=$2 ORDER BY version DESC`,
		agentID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SpecYAML returns the raw canonical JSON of a version (stored as jsonb).
func (s *Store) SpecJSON(ctx context.Context, orgID, agentID string, version int) ([]byte, error) {
	var spec []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT spec FROM agent_versions WHERE agent_id=$1 AND org_id=$2 AND version=$3`,
		agentID, orgID, version).Scan(&spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return spec, err
}

func insertVersion(ctx context.Context, tx *sql.Tx, agentID, orgID string, version int, canon []byte, hash string) (*Version, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`INSERT INTO agent_versions (agent_id, org_id, version, spec, spec_hash, status)
		 VALUES ($1, $2, $3, $4, $5, 'draft') RETURNING id`,
		agentID, orgID, version, canon, hash).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &Version{ID: id, AgentID: agentID, Version: version, Status: "draft", SpecHash: hash}, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func queryVersion(s *Store, ctx context.Context, q string, args ...any) (*Version, error) {
	row := s.db.QueryRowContext(ctx, q, args...)
	return scanVersion(row)
}

func scanVersion(rs rowScanner) (*Version, error) {
	var v Version
	var pub sql.NullTime
	if err := rs.Scan(&v.ID, &v.AgentID, &v.Version, &v.Status, &v.SpecHash, &pub, &v.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if pub.Valid {
		t := pub.Time
		v.PublishedAt = &t
	}
	return &v, nil
}

func isUniqueViolation(err error) bool {
	// pgconn.PgError code 23505 without importing pgconn here.
	type coder interface{ SQLState() string }
	var c coder
	if errors.As(err, &c) {
		return c.SQLState() == "23505"
	}
	return false
}
