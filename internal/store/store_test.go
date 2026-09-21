package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore opens a real SQLite database in a temp directory. There is no
// value in mocking a library that runs in-process and creates a file.
func newTestStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drill.db")
	s, err := Open(t.Context(), path, opts...)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return s
}

func TestOpenCreatesSchema(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	want := []string{"attempt_files", "attempts", "drafts", "notes", "reviews", "schema_migrations", "tasks"}
	rows, err := s.DB().QueryContext(t.Context(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tables = %v, want %v", got, want)
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "deeper", "drill.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	if err := s.Ping(t.Context()); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "drill.db")

	for i := range 3 {
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("Open() #%d error = %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close() #%d error = %v", i, err)
		}
	}

	s := newTestStore(t)
	_ = s

	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer reopened.Close()

	var n int
	if err := reopened.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_migrations rows = %d, want 1 after repeated opens", n)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO attempts (task_id, status, started_at, day) VALUES ('ghost', 'in_progress', '2026-01-01T00:00:00Z', '2026-01-01')`)
	if err == nil {
		t.Fatal("inserting an attempt for a missing task succeeded; foreign keys are off")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("error = %v, want a foreign key violation", err)
	}
}

func TestOneOpenAttemptPerTask(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "slices-01")

	insert := func() error {
		_, err := s.DB().ExecContext(t.Context(),
			`INSERT INTO attempts (task_id, status, started_at, day) VALUES ('slices-01', 'in_progress', '2026-01-01T00:00:00Z', '2026-01-01')`)
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert(); err == nil {
		t.Fatal("second open attempt was accepted; the partial unique index is missing")
	}

	// Closing the first one must free the slot.
	if _, err := s.DB().ExecContext(t.Context(),
		`UPDATE attempts SET status = 'abandoned' WHERE task_id = 'slices-01'`); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err != nil {
		t.Fatalf("insert after closing the previous attempt: %v", err)
	}
}

func TestAttemptFilesCascade(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "slices-01")

	res, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO attempts (task_id, status, started_at, day) VALUES ('slices-01', 'passed', '2026-01-01T00:00:00Z', '2026-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO attempt_files (attempt_id, path, content) VALUES (?, 'main.go', 'package main')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(t.Context(), `DELETE FROM attempts WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM attempt_files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("attempt_files rows after deleting the attempt = %d, want 0", n)
	}
}

func TestDifficultyConstraint(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	for _, d := range []int{0, 4} {
		_, err := s.DB().ExecContext(t.Context(),
			`INSERT INTO tasks (id, title, topic, difficulty, estimate_min, content_hash, first_seen_at, updated_at)
			 VALUES (?, 't', 'slices', ?, 10, 'h', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			"bad", d)
		if err == nil {
			t.Errorf("difficulty %d was accepted, want a CHECK violation", d)
		}
	}
}

func TestLoadMigrations(t *testing.T) {
	t.Parallel()
	got, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no migrations were embedded")
	}
	for i, m := range got {
		if m.sql == "" {
			t.Errorf("migration %d has empty SQL", m.version)
		}
		if i > 0 && got[i-1].version >= m.version {
			t.Errorf("migrations are not sorted ascending: %d before %d", got[i-1].version, m.version)
		}
	}
}

func seedTask(t *testing.T, s *Store, id string) {
	t.Helper()
	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO tasks (id, title, topic, difficulty, estimate_min, content_hash, first_seen_at, updated_at)
		 VALUES (?, ?, 'slices', 1, 10, 'hash', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "Task "+id)
	if err != nil {
		t.Fatalf("seed task %q: %v", id, err)
	}
}
