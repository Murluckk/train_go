package store

import (
	"context"
	"fmt"
)

// TaskMeta is the subset of a task definition that is mirrored into the
// database. The files on disk remain authoritative; this copy exists so that
// statistics can be grouped by topic in SQL.
type TaskMeta struct {
	ID          string
	Title       string
	Topic       string
	Difficulty  int
	EstimateMin int
	ContentHash string
}

// SyncTasks reconciles the mirror with what the catalog found on disk. Tasks
// that are present get upserted, tasks that vanished are soft deleted, and a
// task that reappears is revived with its history intact.
func (s *Store) SyncTasks(ctx context.Context, tasks []TaskMeta) error {
	now := formatTime(s.now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	seen := make([]any, 0, len(tasks))
	for _, t := range tasks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (id, title, topic, difficulty, estimate_min, content_hash, first_seen_at, updated_at, deleted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT (id) DO UPDATE SET
				title        = excluded.title,
				topic        = excluded.topic,
				difficulty   = excluded.difficulty,
				estimate_min = excluded.estimate_min,
				content_hash = excluded.content_hash,
				updated_at   = CASE WHEN tasks.content_hash = excluded.content_hash
				                    THEN tasks.updated_at ELSE excluded.updated_at END,
				deleted_at   = NULL`,
			t.ID, t.Title, t.Topic, t.Difficulty, t.EstimateMin, t.ContentHash, now, now); err != nil {
			return fmt.Errorf("upsert task %q: %w", t.ID, err)
		}
		seen = append(seen, t.ID)
	}

	q := `UPDATE tasks SET deleted_at = ? WHERE deleted_at IS NULL`
	args := []any{now}
	if len(seen) > 0 {
		q += ` AND id NOT IN (` + placeholders(len(seen)) + `)`
		args = append(args, seen...)
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("soft delete missing tasks: %w", err)
	}

	return tx.Commit()
}

// TaskState is the per-task progress summary used by the task list screen.
type TaskState struct {
	TaskMeta
	Deleted      bool
	Attempts     int
	Passes       int
	BestActiveMS int64  // fastest green run, 0 when never solved
	LastActiveMS int64  // most recent green run
	LastPassedAt string // RFC3339, empty when never solved
	Stage        int
	DueOn        Day // empty when not scheduled
}

// TaskStates returns progress for every task, deleted ones included so that
// history is never hidden.
func (s *Store) TaskStates(ctx context.Context) ([]TaskState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.title, t.topic, t.difficulty, t.estimate_min, t.content_hash,
		       t.deleted_at IS NOT NULL,
		       (SELECT count(*) FROM attempts a WHERE a.task_id = t.id AND a.status <> 'abandoned'),
		       (SELECT count(*) FROM attempts a WHERE a.task_id = t.id AND a.status = 'passed'),
		       coalesce((SELECT min(a.active_ms) FROM attempts a WHERE a.task_id = t.id AND a.status = 'passed'), 0),
		       coalesce((SELECT a.active_ms FROM attempts a WHERE a.task_id = t.id AND a.status = 'passed'
		                 ORDER BY a.finished_at DESC LIMIT 1), 0),
		       coalesce(r.last_passed_at, ''), coalesce(r.stage, 0), coalesce(r.due_on, '')
		FROM tasks t
		LEFT JOIN reviews r ON r.task_id = t.id
		ORDER BY t.topic, t.difficulty, t.id`)
	if err != nil {
		return nil, fmt.Errorf("query task states: %w", err)
	}
	defer rows.Close()

	var out []TaskState
	for rows.Next() {
		var ts TaskState
		if err := rows.Scan(&ts.ID, &ts.Title, &ts.Topic, &ts.Difficulty, &ts.EstimateMin, &ts.ContentHash,
			&ts.Deleted, &ts.Attempts, &ts.Passes, &ts.BestActiveMS, &ts.LastActiveMS,
			&ts.LastPassedAt, &ts.Stage, &ts.DueOn); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// TaskPassed reports whether the task has ever been solved. The reference
// solution endpoint is gated on this, server side: hiding it only in the UI
// would defeat the entire point of the exercise.
func (s *Store) TaskPassed(ctx context.Context, taskID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM attempts WHERE task_id = ? AND status = 'passed'`, taskID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, 0, 2*n)
	for i := range n {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '?')
	}
	return string(buf)
}
