package store

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"
)

// SolvePoint is one green run on the progress chart.
type SolvePoint struct {
	AttemptID  int64     `json:"attempt_id"`
	Day        Day       `json:"day"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`
	ActiveMS   int64     `json:"active_ms"`
	RunCount   int       `json:"run_count"`
	FailCount  int       `json:"fail_count"`
	HasNote    bool      `json:"has_note"`
}

// TaskSeries is the history of one task over time.
type TaskSeries struct {
	TaskID     string       `json:"task_id"`
	Title      string       `json:"title"`
	Topic      string       `json:"topic"`
	Difficulty int          `json:"difficulty"`
	Points     []SolvePoint `json:"points"`
	BestMS     int64        `json:"best_ms"`
	LastMS     int64        `json:"last_ms"`
	// TrendPct is how much slower (positive) or faster (negative) the latest
	// solve was compared with the best one, in percent. It is what makes a
	// regression visible without any automatic demotion of the schedule.
	TrendPct int `json:"trend_pct"`
}

// TaskSeriesAll returns the per-task time series behind the progress chart.
func (s *Store) TaskSeriesAll(ctx context.Context) ([]TaskSeries, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.title, t.topic, t.difficulty,
		       a.id, a.day, a.finished_at, a.duration_ms, a.active_ms, a.run_count, a.fail_count,
		       EXISTS (SELECT 1 FROM notes n WHERE n.attempt_id = a.id)
		FROM attempts a
		JOIN tasks t ON t.id = a.task_id
		WHERE a.status = 'passed'
		ORDER BY t.topic, t.id, a.finished_at`)
	if err != nil {
		return nil, fmt.Errorf("query task series: %w", err)
	}
	defer rows.Close()

	var (
		out   []TaskSeries
		index = map[string]int{}
	)
	for rows.Next() {
		var (
			id, title, topic string
			difficulty       int
			p                SolvePoint
			finishedAt       string
		)
		if err := rows.Scan(&id, &title, &topic, &difficulty,
			&p.AttemptID, &p.Day, &finishedAt, &p.DurationMS, &p.ActiveMS,
			&p.RunCount, &p.FailCount, &p.HasNote); err != nil {
			return nil, err
		}
		if p.FinishedAt, err = parseTime(finishedAt); err != nil {
			return nil, err
		}
		i, ok := index[id]
		if !ok {
			i = len(out)
			index[id] = i
			out = append(out, TaskSeries{TaskID: id, Title: title, Topic: topic, Difficulty: difficulty})
		}
		out[i].Points = append(out[i].Points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		s := &out[i]
		s.BestMS = math.MaxInt64
		for _, p := range s.Points {
			s.BestMS = min(s.BestMS, p.ActiveMS)
		}
		s.LastMS = s.Points[len(s.Points)-1].ActiveMS
		if s.BestMS > 0 {
			s.TrendPct = int(math.Round(float64(s.LastMS-s.BestMS) / float64(s.BestMS) * 100))
		}
	}
	return out, nil
}

// TopicStat aggregates everything solved within one topic.
type TopicStat struct {
	Topic          string  `json:"topic"`
	Tasks          int     `json:"tasks"`
	Solves         int     `json:"solves"`
	MedianActiveMS int64   `json:"median_active_ms"`
	P90ActiveMS    int64   `json:"p90_active_ms"`
	AvgFailedRuns  float64 `json:"avg_failed_runs"`
	UnsolvedTasks  int     `json:"unsolved_tasks"`
}

// TopicStats answers the question the stats screen exists for: which topics am
// I slowest in. Percentiles are computed in Go because SQLite has no median.
func (s *Store) TopicStats(ctx context.Context) ([]TopicStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.topic, a.active_ms, a.fail_count
		FROM attempts a
		JOIN tasks t ON t.id = a.task_id
		WHERE a.status = 'passed'`)
	if err != nil {
		return nil, fmt.Errorf("query topic stats: %w", err)
	}
	defer rows.Close()

	type bucket struct {
		durations []int64
		fails     int
	}
	buckets := map[string]*bucket{}
	for rows.Next() {
		var (
			topic  string
			active int64
			fails  int
		)
		if err := rows.Scan(&topic, &active, &fails); err != nil {
			return nil, err
		}
		b, ok := buckets[topic]
		if !ok {
			b = new(bucket)
			buckets[topic] = b
		}
		b.durations = append(b.durations, active)
		b.fails += fails
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts, err := s.topicTaskCounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]TopicStat, 0, len(counts))
	for topic, c := range counts {
		st := TopicStat{Topic: topic, Tasks: c.total, UnsolvedTasks: c.total - c.solved}
		if b := buckets[topic]; b != nil {
			slices.Sort(b.durations)
			st.Solves = len(b.durations)
			st.MedianActiveMS = percentile(b.durations, 0.5)
			st.P90ActiveMS = percentile(b.durations, 0.9)
			st.AvgFailedRuns = float64(b.fails) / float64(len(b.durations))
		}
		out = append(out, st)
	}
	// Slowest topics first: that is the actionable end of the list.
	slices.SortFunc(out, func(a, b TopicStat) int {
		if a.MedianActiveMS != b.MedianActiveMS {
			return int(b.MedianActiveMS - a.MedianActiveMS)
		}
		if a.Topic < b.Topic {
			return -1
		}
		return 1
	})
	return out, nil
}

type topicCount struct{ total, solved int }

func (s *Store) topicTaskCounts(ctx context.Context) (map[string]topicCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.topic, count(*),
		       sum(CASE WHEN EXISTS (SELECT 1 FROM attempts a WHERE a.task_id = t.id AND a.status = 'passed')
		                THEN 1 ELSE 0 END)
		FROM tasks t
		WHERE t.deleted_at IS NULL
		GROUP BY t.topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]topicCount{}
	for rows.Next() {
		var (
			topic         string
			total, solved int
		)
		if err := rows.Scan(&topic, &total, &solved); err != nil {
			return nil, err
		}
		out[topic] = topicCount{total: total, solved: solved}
	}
	return out, rows.Err()
}

// percentile returns the p-quantile of an already sorted slice using nearest
// rank, which is the honest choice for the handful of samples we have.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(idx, 0), len(sorted)-1)]
}

// DailyCount is the number of green runs on one day.
type DailyCount struct {
	Day    Day `json:"day"`
	Solves int `json:"solves"`
}

// DailyActivity returns solves per day over the trailing window, used for the
// activity strip on the stats screen.
func (s *Store) DailyActivity(ctx context.Context, since Day) ([]DailyCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, count(*) FROM attempts WHERE status = 'passed' AND day >= ? GROUP BY day ORDER BY day`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DailyCount
	for rows.Next() {
		var d DailyCount
		if err := rows.Scan(&d.Day, &d.Solves); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
