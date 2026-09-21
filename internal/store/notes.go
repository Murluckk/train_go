package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Note is a "what did I get wrong" entry written after a green run. There is
// at most one per attempt, enforced by a unique index.
type Note struct {
	ID         int64
	AttemptID  int64
	TaskID     string
	TaskTitle  string
	Topic      string
	Body       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	AttemptDay Day
}

// SaveNote creates or replaces the note attached to an attempt.
func (s *Store) SaveNote(ctx context.Context, attemptID int64, body string) (Note, error) {
	now := s.now()

	var taskID string
	err := s.db.QueryRowContext(ctx, `SELECT task_id FROM attempts WHERE id = ?`, attemptID).Scan(&taskID)
	if err != nil {
		return Note{}, wrapNoRows(err, fmt.Errorf("attempt %d: %w", attemptID, ErrNotFound))
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO notes (attempt_id, task_id, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (attempt_id) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		attemptID, taskID, body, formatTime(now), formatTime(now))
	if err != nil {
		return Note{}, fmt.Errorf("save note: %w", err)
	}
	return s.NoteByAttempt(ctx, attemptID)
}

// NoteByAttempt returns the note for an attempt, or ErrNotFound.
func (s *Store) NoteByAttempt(ctx context.Context, attemptID int64) (Note, error) {
	return scanNote(s.db.QueryRowContext(ctx, noteColumns+` WHERE n.attempt_id = ?`, attemptID))
}

// Note returns a single note by id.
func (s *Store) Note(ctx context.Context, id int64) (Note, error) {
	return scanNote(s.db.QueryRowContext(ctx, noteColumns+` WHERE n.id = ?`, id))
}

// Notes lists the journal newest first. A non-empty topic filters by topic.
func (s *Store) Notes(ctx context.Context, topic string, limit, offset int) ([]Note, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		noteColumns+` WHERE (? = '' OR t.topic = ?) ORDER BY n.created_at DESC, n.id DESC LIMIT ? OFFSET ?`,
		topic, topic, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query notes: %w", err)
	}
	defer rows.Close()

	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteNote removes a journal entry.
func (s *Store) DeleteNote(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("note %d: %w", id, ErrNotFound)
	}
	return nil
}

const noteColumns = `
	SELECT n.id, n.attempt_id, n.task_id, t.title, t.topic, n.body, n.created_at, n.updated_at, a.day
	FROM notes n
	JOIN tasks t ON t.id = n.task_id
	JOIN attempts a ON a.id = n.attempt_id`

func scanNote(row rowScanner) (Note, error) {
	var (
		n                    Note
		createdAt, updatedAt string
	)
	err := row.Scan(&n.ID, &n.AttemptID, &n.TaskID, &n.TaskTitle, &n.Topic, &n.Body, &createdAt, &updatedAt, &n.AttemptDay)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, ErrNotFound
	}
	if err != nil {
		return Note{}, err
	}
	if n.CreatedAt, err = parseTime(createdAt); err != nil {
		return Note{}, err
	}
	if n.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Note{}, err
	}
	return n, nil
}
