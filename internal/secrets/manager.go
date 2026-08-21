// Package secrets implements envelope-encrypted secret storage
// (specs/08-secrets.md): a root KEK per environment wraps per-org DEKs;
// DEKs encrypt individual secrets with AES-256-GCM. Every read/write/delete
// emits an audit event.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/prav-j/dark-factory/internal/audit"
)

var (
	ErrNotFound    = errors.New("secret not found")
	ErrWrongTenant = errors.New("secret does not belong to caller")
	ErrNoRootKey   = errors.New("no root key for KEK version")
)

// RootKeyProvider supplies the environment's root keys by version. In
// production this fronts KMS; tests inject static keys.
type RootKeyProvider interface {
	RootKey(version int) ([]byte, error)
}

// StaticRootKeys is an in-memory provider (tests / dev).
type StaticRootKeys struct {
	Keys map[int][]byte
}

func (s StaticRootKeys) RootKey(version int) ([]byte, error) {
	k, ok := s.Keys[version]
	if !ok {
		return nil, ErrNoRootKey
	}
	return k, nil
}

// Manager is the secret store.
type Manager struct {
	DB         *sql.DB
	RootKeys   RootKeyProvider
	KEKVersion int // version used to wrap new DEKs
}

// deriveKEK stretches the root key to an AES-256 key (root keys are already
// high-entropy; SHA-256 normalizes arbitrary-length input).
func deriveKEK(root []byte) ([]byte, error) {
	k := sha256.Sum256(root)
	return k[:], nil
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

func open(key, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], aad)
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// kekFor unwraps the org DEK of the given version using the root key.
// It accepts a tx so callers inside transactions see their own writes.
func (m *Manager) kekFor(ctx context.Context, q rowQuerier, orgID string, dekVersion int) ([]byte, error) {
	var wrapped []byte
	err := q.QueryRowContext(ctx,
		`SELECT ciphertext FROM org_deks WHERE org_id=$1 AND version=$2`,
		orgID, dekVersion).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no DEK v%d for org", dekVersion)
	}
	if err != nil {
		return nil, err
	}
	root, err := m.RootKeys.RootKey(m.KEKVersion)
	if err != nil {
		return nil, err
	}
	kek, err := deriveKEK(root)
	if err != nil {
		return nil, err
	}
	aad := append([]byte(orgID), versionBytes(dekVersion)...)
	return open(kek, wrapped, aad)
}

// ensureDEK returns the org's current DEK, creating one if absent.
func (m *Manager) ensureDEK(ctx context.Context, ex interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, orgID string) (int, error) {
	var version int
	err := m.DB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM org_deks WHERE org_id=$1`, orgID).Scan(&version)
	if err != nil {
		return 0, err
	}
	if version > 0 {
		return version, nil
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return 0, err
	}
	root, err := m.RootKeys.RootKey(m.KEKVersion)
	if err != nil {
		return 0, err
	}
	kek, err := deriveKEK(root)
	if err != nil {
		return 0, err
	}
	wrapped, err := seal(kek, dek, append([]byte(orgID), versionBytes(1)...))
	if err != nil {
		return 0, err
	}
	if _, err := ex.ExecContext(ctx,
		`INSERT INTO org_deks (org_id, version, ciphertext) VALUES ($1, 1, $2)`,
		orgID, wrapped); err != nil {
		return 0, err
	}
	return 1, nil
}

// Put encrypts and stores a secret; returns its ID.
func (m *Manager) Put(ctx context.Context, orgID, userID string, plaintext []byte) (string, error) {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	version, err := m.ensureDEK(ctx, tx, orgID)
	if err != nil {
		return "", err
	}
	dek, err := m.kekFor(ctx, tx, orgID, version)
	if err != nil {
		return "", err
	}
	ct, err := seal(dek, plaintext, []byte(userID))
	if err != nil {
		return "", err
	}

	var id string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO secrets (user_id, org_id, ciphertext, dek_version)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, orgID, ct, version).Scan(&id)
	if err != nil {
		return "", err
	}
	if err := audit.Write(ctx, tx, userID, "secret.put", id, "allow", ""); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// Get decrypts a secret the caller owns. Every read is access-logged.
func (m *Manager) Get(ctx context.Context, orgID, userID, secretID string) ([]byte, error) {
	var ct []byte
	var dekVersion int
	var owner string
	err := m.DB.QueryRowContext(ctx,
		`SELECT ciphertext, dek_version, user_id FROM secrets WHERE id=$1 AND org_id=$2`,
		secretID, orgID).Scan(&ct, &dekVersion, &owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != userID) {
		if writeErr := audit.Write(ctx, m.DB, userID, "secret.get", secretID, "deny",
			"not found or wrong tenant"); writeErr != nil {
			return nil, writeErr
		}
		if err == nil {
			return nil, ErrWrongTenant
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	dek, err := m.kekFor(ctx, m.DB, orgID, dekVersion)
	if err != nil {
		return nil, err
	}
	pt, err := open(dek, ct, []byte(owner))
	if err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, m.DB, userID, "secret.get", secretID, "allow", ""); err != nil {
		return nil, err
	}
	return pt, nil
}

// Delete removes a secret.
func (m *Manager) Delete(ctx context.Context, orgID, userID, secretID string) error {
	tag, err := m.DB.ExecContext(ctx,
		`DELETE FROM secrets WHERE id=$1 AND org_id=$2 AND user_id=$3`,
		secretID, orgID, userID)
	if err != nil {
		return err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return audit.Write(ctx, m.DB, userID, "secret.delete", secretID, "allow", "")
}

// RotateOrgDEK generates a fresh DEK version and re-encrypts every secret in
// the org under it, atomically.
func (m *Manager) RotateOrgDEK(ctx context.Context, orgID, actor string) (int, error) {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var current int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM org_deks WHERE org_id=$1`, orgID).Scan(&current); err != nil {
		return 0, err
	}
	next := current + 1

	newDEK := make([]byte, 32)
	if _, err := rand.Read(newDEK); err != nil {
		return 0, err
	}
	root, err := m.RootKeys.RootKey(m.KEKVersion)
	if err != nil {
		return 0, err
	}
	kek, err := deriveKEK(root)
	if err != nil {
		return 0, err
	}
	wrapped, err := seal(kek, newDEK, append([]byte(orgID), versionBytes(next)...))
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO org_deks (org_id, version, ciphertext) VALUES ($1, $2, $3)`,
		orgID, next, wrapped); err != nil {
		return 0, err
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, user_id, ciphertext, dek_version FROM secrets WHERE org_id=$1`, orgID)
	if err != nil {
		return 0, err
	}
	type reenc struct {
		id, user string
		ct       []byte
		fromVer  int
	}
	var batch []reenc
	for rows.Next() {
		var r reenc
		if err := rows.Scan(&r.id, &r.user, &r.ct, &r.fromVer); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, r)
	}
	rows.Close()

	for _, r := range batch {
		oldDEK, err := m.kekFor(ctx, tx, orgID, r.fromVer)
		if err != nil {
			return 0, err
		}
		pt, err := open(oldDEK, r.ct, []byte(r.user))
		if err != nil {
			return 0, fmt.Errorf("re-encrypt %s: %w", r.id, err)
		}
		newCT, err := seal(newDEK, pt, []byte(r.user))
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE secrets SET ciphertext=$1, dek_version=$2 WHERE id=$3`,
			newCT, next, r.id); err != nil {
			return 0, err
		}
	}

	if err := audit.Write(ctx, tx, actor, "secret.rotate_dek", orgID, "allow",
		fmt.Sprintf("version=%d secrets=%d", next, len(batch))); err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

func versionBytes(v int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b
}
