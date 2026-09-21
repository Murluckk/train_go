package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// Attempt statuses.
const (
	StatusInProgress = "in_progress"
	StatusPassed     = "passed"
	StatusAbandoned  = "abandoned"
)

// ResumeWindow is how long an unfinished attempt stays resumable. Reloading the
// page must not restart the clock, but coming back the next morning must not
// inherit yesterday's stopwatch either.
const ResumeWindow = 4 * time.Hour

// Attempt is one sitting with a task.
type Attempt struct {
	ID         int64
	TaskID     string
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time // zero while in progress
	DurationMS int64     // wall clock to the first green run
	ActiveMS   int64     // time the editor was actually focused and typed in
	Day        Day
	RunCount   int
	FailCount  int
}

// InProgress reports whether the attempt is still open.
func (a Attempt) InProgress() bool { return a.Status == StatusInProgress }

// StartAttempt opens an attempt for the task, resuming a recent unfinished one
// instead of starting over. The second return value reports whether an existing
// attempt was resumed.
func (s *Store) StartAttempt(ctx context.Context, taskID string) (Attempt, bool, error) {
	now := s.now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	open, err := scanAttempt(tx.QueryRowContext(ctx, attemptColumns+`
		FROM attempts WHERE task_id = ? AND status = 'in_progress'`, taskID))
	switch {
	case err == nil && now.Sub(open.StartedAt) < ResumeWindow:
		return open, true, tx.Commit()
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE attempts SET status = 'abandoned', finished_at = ? WHERE id = ?`,
			formatTime(now), open.ID); err != nil {
			return Attempt{}, false, fmt.Errorf("abandon stale attempt: %w", err)
		}
	case !errors.Is(err, ErrNotFound):
		return Attempt{}, false, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO attempts (task_id, status, started_at, day) VALUES (?, 'in_progress', ?, ?)`,
		taskID, formatTime(now), formatDay(now))
	if err != nil {
		return Attempt{}, false, fmt.Errorf("insert attempt: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Attempt{}, false, err
	}
	created, err := scanAttempt(tx.QueryRowContext(ctx, attemptColumns+` FROM attempts WHERE id = ?`, id))
	if err != nil {
		return Attempt{}, false, err
	}
	return created, false, tx.Commit()
}

// Attempt looks up a single attempt.
func (s *Store) Attempt(ctx context.Context, id int64) (Attempt, error) {
	return scanAttempt(s.db.QueryRowContext(ctx, attemptColumns+` FROM attempts WHERE id = ?`, id))
}

// AbandonAttempt closes an open attempt without recording a solve.
func (s *Store) AbandonAttempt(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE attempts SET status = 'abandoned', finished_at = ? WHERE id = ? AND status = 'in_progress'`,
		formatTime(s.now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("abandon attempt %d: %w", id, ErrNotFound)
	}
	return nil
}

// AddActive credits the attempt with focused editing time. The client reports
// it in small increments, so this is an accumulate, not a set.
func (s *Store) AddActive(ctx context.Context, id int64, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE attempts SET active_ms = active_ms + ? WHERE id = ? AND status = 'in_progress'`,
		d.Milliseconds(), id)
	return err
}

// RecordRun counts a test run against the attempt. A red run never stops the
// clock; it only tells us how many rounds the solve took.
func (s *Store) RecordRun(ctx context.Context, id int64, passed bool) error {
	fail := 0
	if !passed {
		fail = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE attempts SET run_count = run_count + 1, fail_count = fail_count + ? WHERE id = ?`,
		fail, id)
	return err
}

// Review is the spaced repetition state of a task.
type Review struct {
	TaskID       string
	Stage        int
	DueOn        Day
	LastPassedAt time.Time
	PassCount    int
	Exists       bool
}

// PassAttempt closes an attempt as solved, snapshots the code that solved it
// and reschedules the task, all in one transaction. advance receives the
// current review state and returns the next one; the ladder itself lives in
// package srs so that the store stays free of policy.
func (s *Store) PassAttempt(ctx context.Context, id int64, files map[string]string, advance func(Review, time.Time) Review) (Attempt, Review, error) {
	now := s.now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, Review{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	att, err := scanAttempt(tx.QueryRowContext(ctx, attemptColumns+` FROM attempts WHERE id = ?`, id))
	if err != nil {
		return Attempt{}, Review{}, err
	}
	if att.Status == StatusPassed {
		// Already green. Re-running tests after solving must not move the
		// schedule or overwrite the snapshot.
		rev, err := reviewOf(ctx, tx, att.TaskID)
		if err != nil {
			return Attempt{}, Review{}, err
		}
		return att, rev, tx.Commit()
	}

	duration := now.Sub(att.StartedAt)
	if duration < 0 {
		duration = 0
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE attempts SET status = 'passed', finished_at = ?, duration_ms = ? WHERE id = ?`,
		formatTime(now), duration.Milliseconds(), id); err != nil {
		return Attempt{}, Review{}, fmt.Errorf("close attempt: %w", err)
	}

	// An attempt where the user never typed (solution pasted in one go, or the
	// heartbeat never fired) would otherwise plot as zero. Fall back to wall
	// clock so the chart cannot silently lie in the flattering direction.
	if att.ActiveMS == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE attempts SET active_ms = ? WHERE id = ?`,
			duration.Milliseconds(), id); err != nil {
			return Attempt{}, Review{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM attempt_files WHERE attempt_id = ?`, id); err != nil {
		return Attempt{}, Review{}, err
	}
	for _, path := range slices.Sorted(maps.Keys(files)) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO attempt_files (attempt_id, path, content) VALUES (?, ?, ?)`,
			id, path, files[path]); err != nil {
			return Attempt{}, Review{}, fmt.Errorf("snapshot %q: %w", path, err)
		}
	}

	cur, err := reviewOf(ctx, tx, att.TaskID)
	if err != nil {
		return Attempt{}, Review{}, err
	}
	next := advance(cur, now)
	next.TaskID = att.TaskID
	next.LastPassedAt = now
	next.PassCount = cur.PassCount + 1
	next.Exists = true

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reviews (task_id, stage, due_on, last_passed_at, pass_count)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (task_id) DO UPDATE SET
			stage = excluded.stage, due_on = excluded.due_on,
			last_passed_at = excluded.last_passed_at, pass_count = excluded.pass_count`,
		next.TaskID, next.Stage, next.DueOn, formatTime(next.LastPassedAt), next.PassCount); err != nil {
		return Attempt{}, Review{}, fmt.Errorf("schedule review: %w", err)
	}

	updated, err := scanAttempt(tx.QueryRowContext(ctx, attemptColumns+` FROM attempts WHERE id = ?`, id))
	if err != nil {
		return Attempt{}, Review{}, err
	}
	return updated, next, tx.Commit()
}

// AttemptFiles returns the code snapshot taken when the attempt went green.
func (s *Store) AttemptFiles(ctx context.Context, id int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, content FROM attempt_files WHERE attempt_id = ? ORDER BY path`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var path, content string
		if err := rows.Scan(&path, &content); err != nil {
			return nil, err
		}
		out[path] = content
	}
	return out, rows.Err()
}

// SaveDraft stores the editor buffers for a task so a reload loses nothing.
func (s *Store) SaveDraft(ctx context.Context, taskID string, files map[string]string) error {
	now := formatTime(s.now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, `DELETE FROM drafts WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for _, path := range slices.Sorted(maps.Keys(files)) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO drafts (task_id, path, content, updated_at) VALUES (?, ?, ?, ?)`,
			taskID, path, files[path], now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Draft returns the saved editor buffers for a task, or an empty map.
func (s *Store) Draft(ctx context.Context, taskID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, content FROM drafts WHERE task_id = ? ORDER BY path`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var path, content string
		if err := rows.Scan(&path, &content); err != nil {
			return nil, err
		}
		out[path] = content
	}
	return out, rows.Err()
}

// ClearDraft drops the saved buffers for a task, used when starting over.
func (s *Store) ClearDraft(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM drafts WHERE task_id = ?`, taskID)
	return err
}

const attemptColumns = `SELECT id, task_id, status, started_at, coalesce(finished_at, ''),
	coalesce(duration_ms, 0), active_ms, day, run_count, fail_count`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAttempt(row rowScanner) (Attempt, error) {
	var (
		a                     Attempt
		startedAt, finishedAt string
	)
	err := row.Scan(&a.ID, &a.TaskID, &a.Status, &startedAt, &finishedAt,
		&a.DurationMS, &a.ActiveMS, &a.Day, &a.RunCount, &a.FailCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, err
	}
	if a.StartedAt, err = parseTime(startedAt); err != nil {
		return Attempt{}, fmt.Errorf("attempt %d: parse started_at: %w", a.ID, err)
	}
	if finishedAt != "" {
		if a.FinishedAt, err = parseTime(finishedAt); err != nil {
			return Attempt{}, fmt.Errorf("attempt %d: parse finished_at: %w", a.ID, err)
		}
	}
	return a, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func reviewOf(ctx context.Context, q querier, taskID string) (Review, error) {
	var (
		r            Review
		lastPassedAt string
	)
	err := q.QueryRowContext(ctx,
		`SELECT task_id, stage, due_on, last_passed_at, pass_count FROM reviews WHERE task_id = ?`, taskID).
		Scan(&r.TaskID, &r.Stage, &r.DueOn, &lastPassedAt, &r.PassCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Review{TaskID: taskID}, nil
	}
	if err != nil {
		return Review{}, err
	}
	if r.LastPassedAt, err = parseTime(lastPassedAt); err != nil {
		return Review{}, fmt.Errorf("review %q: parse last_passed_at: %w", taskID, err)
	}
	r.Exists = true
	return r, nil
}
