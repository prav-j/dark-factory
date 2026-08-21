// Package db owns control-plane schema migrations (golang-migrate, embedded
// SQL) and database access helpers.
package db

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrateUp applies all pending migrations. Safe to call repeatedly.
func MigrateUp(dsn string) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsnToMigrateURL(dsn))
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// dsnToMigrateURL converts a postgres:// DSN into the pgx driver URL
// golang-migrate expects (pgx5://...).
func dsnToMigrateURL(dsn string) string {
	const prefix = "postgres://"
	if len(dsn) >= len(prefix) && dsn[:len(prefix)] == prefix {
		return "pgx5://" + dsn[len(prefix):]
	}
	return dsn
}
