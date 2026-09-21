package store

import (
	"context"
	"fmt"
)

// DueReview is a scheduled repetition that has come up.
type DueReview struct {
	TaskID    string
	Title     string
	Topic     string
	DueOn     Day
	Stage     int
	PassCount int
	OverdueBy int // whole days past due; 0 means due today
}

// DueReviews returns every repetition due on or before today, oldest first.
func (s *Store) DueReviews(ctx context.Context, today Day) ([]DueReview, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.task_id, t.title, t.topic, r.due_on, r.stage, r.pass_count,
		       CAST(julianday(?) - julianday(r.due_on) AS INTEGER)
		FROM reviews r
		JOIN tasks t ON t.id = r.task_id
		WHERE r.due_on <= ? AND t.deleted_at IS NULL
		ORDER BY r.due_on, t.difficulty, r.task_id`, today, today)
	if err != nil {
		return nil, fmt.Errorf("query due reviews: %w", err)
	}
	defer rows.Close()

	var out []DueReview
	for rows.Next() {
		var d DueReview
		if err := rows.Scan(&d.TaskID, &d.Title, &d.Topic, &d.DueOn, &d.Stage, &d.PassCount, &d.OverdueBy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Review returns the schedule state of a task. A task that was never solved
// yields a zero Review with Exists false rather than ErrNotFound: "not
// scheduled yet" is a normal state, not a failure.
func (s *Store) Review(ctx context.Context, taskID string) (Review, error) {
	return reviewOf(ctx, s.db, taskID)
}

// PickNewTask chooses the single new task to offer today. The choice is
// deterministic so that refreshing the page does not reshuffle it: least
// practised topic first, then easiest, then by id.
func (s *Store) PickNewTask(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id
		FROM tasks t
		LEFT JOIN reviews r ON r.task_id = t.id
		WHERE t.deleted_at IS NULL AND r.task_id IS NULL
		ORDER BY (SELECT count(*) FROM attempts a
		          JOIN tasks t2 ON t2.id = a.task_id
		          WHERE t2.topic = t.topic AND a.status = 'passed'),
		         t.difficulty, t.id
		LIMIT 1`).Scan(&id)
	if err != nil {
		return "", wrapNoRows(err, ErrNotFound)
	}
	return id, nil
}

// SolvedToday counts the tasks solved on the given local day. It drives the
// streak on the home screen.
func (s *Store) SolvedToday(ctx context.Context, day Day) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(DISTINCT task_id) FROM attempts WHERE status = 'passed' AND day = ?`, day).Scan(&n)
	return n, err
}

// Streak counts consecutive days ending today (or yesterday, if nothing has
// been solved today yet) on which at least one task went green.
func (s *Store) Streak(ctx context.Context, today Day) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT day FROM attempts WHERE status = 'passed' AND day <= ? ORDER BY day DESC`, today)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var days []Day
	for rows.Next() {
		var d Day
		if err := rows.Scan(&d); err != nil {
			return 0, err
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(days) == 0 {
		return 0, nil
	}

	now := s.now()
	// Not having practised yet today should not read as a broken streak until
	// the day is actually over.
	expect := DayOf(now)
	if days[0] != expect {
		expect = AddDays(now, -1)
		if days[0] != expect {
			return 0, nil
		}
	}

	streak := 0
	for _, d := range days {
		if d != expect {
			break
		}
		streak++
		t, err := parseDay(d)
		if err != nil {
			return 0, err
		}
		expect = AddDays(t, -1)
	}
	return streak, nil
}
