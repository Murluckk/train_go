// Package store owns the SQLite database: connection, migrations and every
// query the trainer runs.
//
// The database is deliberately limited to a single connection. There is one
// user, so there is no read concurrency to lose, and a single connection makes
// SQLITE_BUSY structurally impossible instead of merely unlikely.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, registered as "sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned by lookups that address a single row by identity.
var ErrNotFound = errors.New("not found")

// Store is a handle on the trainer database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Option customises a Store at construction time.
type Option func(*Store)

// WithClock replaces the time source. Tests use it to make dates deterministic.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// Open opens (creating it if needed) the database at path and brings the schema
// up to date.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One writer, no readers waiting on it: see the package comment.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := s.verifyPragmas(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func dsn(path string) string {
	q := make(url.Values)
	// WAL keeps a long-running read from blocking a write; busy_timeout is a
	// belt-and-braces guard for the moment a second process (say, the sqlite3
	// CLI) touches the file.
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(normal)")
	u := url.URL{Scheme: "file", Opaque: filepath.ToSlash(path), RawQuery: q.Encode()}
	return u.String()
}

// verifyPragmas fails loudly if the driver silently ignored the DSN. Foreign
// keys defaulting to off would let attempt_files outlive their attempt, and the
// bug would only surface much later as orphaned rows.
func (s *Store) verifyPragmas(ctx context.Context) error {
	var fk int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	if fk != 1 {
		return errors.New("foreign_keys pragma is off; the DSN was not applied")
	}
	return nil
}

// DB exposes the underlying handle. It exists for tests and ad hoc queries;
// production code should use the methods on Store.
func (s *Store) DB() *sql.DB { return s.db }

// Now returns the store's notion of the current time.
func (s *Store) Now() time.Time { return s.now() }

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

type migration struct {
	version int
	name    string
	sql     string
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if slices.Contains(applied, m.version) {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

func (s *Store) appliedVersions(ctx context.Context) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the commit succeeds

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, formatTime(s.now())); err != nil {
		return err
	}
	return tx.Commit()
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}

	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: want NNNN_name.sql", e.Name())
		}
		v, err := strconv.Atoi(version)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version prefix: %w", e.Name(), err)
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}

	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

// wrapNoRows translates the driver's sentinel into the package's own.
func wrapNoRows(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}
	return err
}
