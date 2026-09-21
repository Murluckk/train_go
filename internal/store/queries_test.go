package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a hand-wound time source: every date in the trainer is derived from
// "now", so tests need to control it rather than sleep.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(s string) *clock {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		panic(err)
	}
	return &clock{t: t}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func storeAt(t *testing.T, c *clock) *Store {
	t.Helper()
	return newTestStore(t, WithClock(c.Now))
}

func meta(id, topic string, difficulty int) TaskMeta {
	return TaskMeta{ID: id, Title: strings.ToUpper(id), Topic: topic,
		Difficulty: difficulty, EstimateMin: 10, ContentHash: "h-" + id}
}

// ladder is a stand-in for package srs so that store tests stay independent of
// the scheduling policy.
func ladder(days ...int) func(Review, time.Time) Review {
	return func(cur Review, now time.Time) Review {
		stage := min(cur.Stage, len(days)-1)
		return Review{Stage: min(cur.Stage+1, len(days)-1), DueOn: AddDays(now, days[stage])}
	}
}

func TestSyncTasks(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)

	if err := s.SyncTasks(t.Context(), []TaskMeta{meta("a", "slices", 1), meta("b", "maps", 2)}); err != nil {
		t.Fatalf("SyncTasks: %v", err)
	}
	states, err := s.TaskStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("got %d tasks, want 2", len(states))
	}

	// b disappears from disk: soft deleted, not dropped.
	if err := s.SyncTasks(t.Context(), []TaskMeta{meta("a", "slices", 1)}); err != nil {
		t.Fatal(err)
	}
	states, err = s.TaskStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("got %d tasks, want 2 (the missing one must be kept)", len(states))
	}
	byID := map[string]TaskState{}
	for _, st := range states {
		byID[st.ID] = st
	}
	if !byID["b"].Deleted {
		t.Error("task b should be marked deleted")
	}
	if byID["a"].Deleted {
		t.Error("task a should not be marked deleted")
	}

	// b comes back.
	if err := s.SyncTasks(t.Context(), []TaskMeta{meta("a", "slices", 1), meta("b", "maps", 2)}); err != nil {
		t.Fatal(err)
	}
	states, _ = s.TaskStates(t.Context())
	for _, st := range states {
		if st.Deleted {
			t.Errorf("task %s should have been revived", st.ID)
		}
	}
}

func TestSyncTasksEmptyDirectorySoftDeletesEverything(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)

	if err := s.SyncTasks(t.Context(), []TaskMeta{meta("a", "slices", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncTasks(t.Context(), nil); err != nil {
		t.Fatalf("SyncTasks(nil): %v", err)
	}
	states, _ := s.TaskStates(t.Context())
	if len(states) != 1 || !states[0].Deleted {
		t.Errorf("states = %+v, want the single task soft deleted", states)
	}
}

func TestStartAttemptResumesWithinWindow(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	first, resumed, err := s.StartAttempt(t.Context(), "a")
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if resumed {
		t.Error("the first attempt cannot be a resume")
	}

	c.Advance(ResumeWindow - time.Minute)
	second, resumed, err := s.StartAttempt(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if !resumed || second.ID != first.ID {
		t.Errorf("got attempt %d resumed=%v, want the same attempt %d resumed", second.ID, resumed, first.ID)
	}
	if !second.StartedAt.Equal(first.StartedAt) {
		t.Error("resuming must not restart the clock")
	}
}

func TestStartAttemptAbandonsStaleAttempt(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	first, _, err := s.StartAttempt(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}

	c.Advance(ResumeWindow + time.Minute)
	second, resumed, err := s.StartAttempt(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if resumed {
		t.Error("an attempt older than the resume window must not be resumed")
	}
	if second.ID == first.ID {
		t.Fatal("a new attempt should have been created")
	}

	stale, err := s.Attempt(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != StatusAbandoned {
		t.Errorf("stale attempt status = %q, want abandoned", stale.Status)
	}
}

func TestPassAttempt(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	att, _, err := s.StartAttempt(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddActive(t.Context(), att.ID, 7*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRun(t.Context(), att.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRun(t.Context(), att.ID, true); err != nil {
		t.Fatal(err)
	}

	c.Advance(11 * time.Minute)
	files := map[string]string{"main.go": "package solution", "helper.go": "package solution"}
	passed, review, err := s.PassAttempt(t.Context(), att.ID, files, ladder(1, 3, 7, 21))
	if err != nil {
		t.Fatalf("PassAttempt: %v", err)
	}

	if passed.Status != StatusPassed {
		t.Errorf("status = %q, want passed", passed.Status)
	}
	if want := (11 * time.Minute).Milliseconds(); passed.DurationMS != want {
		t.Errorf("DurationMS = %d, want %d", passed.DurationMS, want)
	}
	if want := (7 * time.Minute).Milliseconds(); passed.ActiveMS != want {
		t.Errorf("ActiveMS = %d, want %d", passed.ActiveMS, want)
	}
	if passed.RunCount != 2 || passed.FailCount != 1 {
		t.Errorf("runs = %d fails = %d, want 2 and 1", passed.RunCount, passed.FailCount)
	}
	if review.Stage != 1 || review.DueOn != "2026-03-02" {
		t.Errorf("review = %+v, want stage 1 due 2026-03-02", review)
	}

	got, err := s.AttemptFiles(t.Context(), att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["main.go"] != "package solution" {
		t.Errorf("snapshot = %v, want both files stored", got)
	}
}

func TestPassAttemptWithoutHeartbeatFallsBackToWallClock(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	att, _, _ := s.StartAttempt(t.Context(), "a")
	c.Advance(4 * time.Minute)

	passed, _, err := s.PassAttempt(t.Context(), att.ID, nil, ladder(1, 3, 7, 21))
	if err != nil {
		t.Fatal(err)
	}
	if want := (4 * time.Minute).Milliseconds(); passed.ActiveMS != want {
		t.Errorf("ActiveMS = %d, want the wall clock %d; a zero would flatter the chart", passed.ActiveMS, want)
	}
}

func TestPassAttemptIsIdempotent(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	att, _, _ := s.StartAttempt(t.Context(), "a")
	c.Advance(5 * time.Minute)
	_, first, err := s.PassAttempt(t.Context(), att.ID, map[string]string{"main.go": "v1"}, ladder(1, 3, 7, 21))
	if err != nil {
		t.Fatal(err)
	}

	// Running the tests again after solving must not move the schedule on.
	c.Advance(30 * time.Minute)
	again, second, err := s.PassAttempt(t.Context(), att.ID, map[string]string{"main.go": "v2"}, ladder(1, 3, 7, 21))
	if err != nil {
		t.Fatal(err)
	}
	if second.Stage != first.Stage || second.DueOn != first.DueOn {
		t.Errorf("review moved from %+v to %+v on a repeat green run", first, second)
	}
	if want := (5 * time.Minute).Milliseconds(); again.DurationMS != want {
		t.Errorf("DurationMS = %d, want the original %d", again.DurationMS, want)
	}
	files, _ := s.AttemptFiles(t.Context(), att.ID)
	if files["main.go"] != "v1" {
		t.Errorf("snapshot = %q, want the code that actually solved it", files["main.go"])
	}
}

func TestLadderAdvancesAcrossSittings(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-01 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))
	advance := ladder(1, 3, 7, 21)

	// Each sitting is a day later, so the due date is relative to that sitting,
	// not to the first one. The last two share a due date because the ladder
	// tops out at 21 days and keeps repeating instead of graduating the task.
	wantDue := []string{"2026-03-02", "2026-03-05", "2026-03-10", "2026-03-25", "2026-03-26"}
	for i, want := range wantDue {
		att, _, err := s.StartAttempt(t.Context(), "a")
		if err != nil {
			t.Fatalf("sitting %d: %v", i, err)
		}
		c.Advance(time.Minute)
		_, review, err := s.PassAttempt(t.Context(), att.ID, nil, advance)
		if err != nil {
			t.Fatalf("sitting %d: %v", i, err)
		}
		if review.DueOn != want {
			t.Errorf("sitting %d: due %s, want %s", i, review.DueOn, want)
		}
		if review.PassCount != i+1 {
			t.Errorf("sitting %d: pass count %d, want %d", i, review.PassCount, i+1)
		}
		// Next day, same task again.
		c.Advance(24 * time.Hour)
	}
}

func TestDueReviews(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1), meta("b", "maps", 2), meta("c", "errors", 3))

	seedReview(t, s, "a", 1, "2026-03-05") // overdue by 5
	seedReview(t, s, "b", 2, "2026-03-10") // due today
	seedReview(t, s, "c", 3, "2026-03-20") // not yet

	due, err := s.DueReviews(t.Context(), "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("got %d due, want 2: %+v", len(due), due)
	}
	if due[0].TaskID != "a" || due[0].OverdueBy != 5 {
		t.Errorf("first due = %+v, want task a overdue by 5", due[0])
	}
	if due[1].TaskID != "b" || due[1].OverdueBy != 0 {
		t.Errorf("second due = %+v, want task b due today", due[1])
	}
}

func TestDueReviewsSkipsDeletedTasks(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))
	seedReview(t, s, "a", 1, "2026-03-01")

	if err := s.SyncTasks(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueReviews(t.Context(), "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("got %+v, want nothing due for a task that is gone from disk", due)
	}
}

func TestPickNewTaskIsDeterministic(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("hard", "slices", 3), meta("easy", "slices", 1), meta("mid", "slices", 2))

	for range 3 {
		id, err := s.PickNewTask(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if id != "easy" {
			t.Fatalf("PickNewTask = %q, want the easiest task; refreshing must not reshuffle", id)
		}
	}
}

func TestPickNewTaskPrefersLeastPractisedTopic(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s,
		meta("slices-done", "slices", 1), meta("slices-next", "slices", 1),
		meta("errors-next", "errors", 2))

	att, _, _ := s.StartAttempt(t.Context(), "slices-done")
	c.Advance(time.Minute)
	if _, _, err := s.PassAttempt(t.Context(), att.ID, nil, ladder(1, 3, 7, 21)); err != nil {
		t.Fatal(err)
	}

	id, err := s.PickNewTask(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if id != "errors-next" {
		t.Errorf("PickNewTask = %q, want the untouched topic even though it is harder", id)
	}
}

func TestPickNewTaskExhausted(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	att, _, _ := s.StartAttempt(t.Context(), "a")
	c.Advance(time.Minute)
	if _, _, err := s.PassAttempt(t.Context(), att.ID, nil, ladder(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PickNewTask(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Errorf("PickNewTask error = %v, want ErrNotFound once everything is scheduled", err)
	}
}

func TestStreak(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		days []string
		want int
	}{
		{name: "nothing solved", want: 0},
		{name: "today only", days: []string{"2026-03-10"}, want: 1},
		{name: "three in a row ending today", days: []string{"2026-03-08", "2026-03-09", "2026-03-10"}, want: 3},
		{name: "ending yesterday still counts", days: []string{"2026-03-08", "2026-03-09"}, want: 2},
		{name: "gap breaks it", days: []string{"2026-03-05", "2026-03-09", "2026-03-10"}, want: 2},
		{name: "stale history", days: []string{"2026-03-01", "2026-03-02"}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newClock("2026-03-10 09:00:00")
			s := storeAt(t, c)
			mustSync(t, s, meta("a", "slices", 1))
			for i, d := range tt.days {
				seedPassedAttempt(t, s, "a", d, int64(i+1)*1000)
			}
			got, err := s.Streak(t.Context(), "2026-03-10")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Streak() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDrafts(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	if err := s.SaveDraft(t.Context(), "a", map[string]string{"main.go": "v1", "aux.go": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDraft(t.Context(), "a", map[string]string{"main.go": "v2"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Draft(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["main.go"] != "v2" {
		t.Errorf("draft = %v, want only the latest buffers", got)
	}
	if err := s.ClearDraft(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Draft(t.Context(), "a"); len(got) != 0 {
		t.Errorf("draft after clear = %v, want empty", got)
	}
}

func TestNotes(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	att, _, _ := s.StartAttempt(t.Context(), "a")
	c.Advance(time.Minute)
	if _, _, err := s.PassAttempt(t.Context(), att.ID, nil, ladder(1)); err != nil {
		t.Fatal(err)
	}

	note, err := s.SaveNote(t.Context(), att.ID, "забыл про nil-слайс")
	if err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if note.TaskTitle != "A" || note.Topic != "slices" {
		t.Errorf("note = %+v, want the task title and topic joined in", note)
	}

	updated, err := s.SaveNote(t.Context(), att.ID, "исправлено")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != note.ID {
		t.Errorf("saving twice created note %d instead of updating %d", updated.ID, note.ID)
	}

	list, err := s.Notes(t.Context(), "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Body != "исправлено" {
		t.Errorf("notes = %+v, want one updated entry", list)
	}

	if list, _ := s.Notes(t.Context(), "maps", 10, 0); len(list) != 0 {
		t.Errorf("filtering by another topic returned %+v", list)
	}

	if err := s.DeleteNote(t.Context(), note.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Note(t.Context(), note.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Note after delete error = %v, want ErrNotFound", err)
	}
}

func TestSaveNoteForMissingAttempt(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	if _, err := s.SaveNote(t.Context(), 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestTopicStats(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("s1", "slices", 1), meta("s2", "slices", 1), meta("e1", "errors", 2))

	seedPassedAttempt(t, s, "s1", "2026-03-08", 60_000)
	seedPassedAttempt(t, s, "s1", "2026-03-09", 120_000)
	seedPassedAttempt(t, s, "s2", "2026-03-09", 300_000)
	seedPassedAttempt(t, s, "e1", "2026-03-09", 30_000)

	stats, err := s.TopicStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d topics, want 2", len(stats))
	}
	if stats[0].Topic != "slices" {
		t.Errorf("first topic = %q, want the slowest one first", stats[0].Topic)
	}
	if stats[0].MedianActiveMS != 120_000 {
		t.Errorf("slices median = %d, want 120000", stats[0].MedianActiveMS)
	}
	if stats[0].Solves != 3 || stats[0].Tasks != 2 || stats[0].UnsolvedTasks != 0 {
		t.Errorf("slices stat = %+v", stats[0])
	}
	if stats[1].Topic != "errors" || stats[1].MedianActiveMS != 30_000 {
		t.Errorf("errors stat = %+v", stats[1])
	}
}

func TestTaskSeriesTrend(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	seedPassedAttempt(t, s, "a", "2026-03-08", 200_000)
	seedPassedAttempt(t, s, "a", "2026-03-09", 100_000)
	seedPassedAttempt(t, s, "a", "2026-03-10", 150_000)

	series, err := s.TaskSeriesAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}
	got := series[0]
	if len(got.Points) != 3 {
		t.Fatalf("got %d points, want 3", len(got.Points))
	}
	if got.BestMS != 100_000 || got.LastMS != 150_000 {
		t.Errorf("best = %d last = %d, want 100000 and 150000", got.BestMS, got.LastMS)
	}
	if got.TrendPct != 50 {
		t.Errorf("TrendPct = %d, want 50 (a visible regression)", got.TrendPct)
	}
}

func TestPercentile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		sorted []int64
		p      float64
		want   int64
	}{
		{name: "empty", p: 0.5},
		{name: "single", sorted: []int64{7}, p: 0.5, want: 7},
		{name: "median of three", sorted: []int64{1, 2, 3}, p: 0.5, want: 2},
		{name: "median of four", sorted: []int64{1, 2, 3, 4}, p: 0.5, want: 2},
		{name: "p90 of ten", sorted: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, p: 0.9, want: 9},
		{name: "p100", sorted: []int64{1, 2, 3}, p: 1, want: 3},
		{name: "p0", sorted: []int64{1, 2, 3}, p: 0, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := percentile(tt.sorted, tt.p); got != tt.want {
				t.Errorf("percentile(%v, %v) = %d, want %d", tt.sorted, tt.p, got, tt.want)
			}
		})
	}
}

func TestTaskPassedGatesSolution(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	mustSync(t, s, meta("a", "slices", 1))

	if ok, err := s.TaskPassed(t.Context(), "a"); err != nil || ok {
		t.Fatalf("TaskPassed = %v, %v; want false before the first green run", ok, err)
	}
	att, _, _ := s.StartAttempt(t.Context(), "a")
	c.Advance(time.Minute)
	if _, _, err := s.PassAttempt(t.Context(), att.ID, nil, ladder(1)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TaskPassed(t.Context(), "a"); err != nil || !ok {
		t.Fatalf("TaskPassed = %v, %v; want true after solving", ok, err)
	}
}

func TestAttemptNotFound(t *testing.T) {
	t.Parallel()
	c := newClock("2026-03-10 09:00:00")
	s := storeAt(t, c)
	if _, err := s.Attempt(t.Context(), 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if err := s.AbandonAttempt(t.Context(), 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func mustSync(t *testing.T, s *Store, tasks ...TaskMeta) {
	t.Helper()
	if err := s.SyncTasks(t.Context(), tasks); err != nil {
		t.Fatalf("SyncTasks: %v", err)
	}
}

func seedReview(t *testing.T, s *Store, taskID string, stage int, due Day) {
	t.Helper()
	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO reviews (task_id, stage, due_on, last_passed_at, pass_count) VALUES (?, ?, ?, ?, 1)`,
		taskID, stage, due, "2026-03-01T09:00:00Z")
	if err != nil {
		t.Fatalf("seed review: %v", err)
	}
}

func seedPassedAttempt(t *testing.T, s *Store, taskID string, day Day, activeMS int64) {
	t.Helper()
	ts := day + "T09:00:00Z"
	_, err := s.DB().ExecContext(t.Context(), `
		INSERT INTO attempts (task_id, status, started_at, finished_at, duration_ms, active_ms, day, run_count, fail_count)
		VALUES (?, 'passed', ?, ?, ?, ?, ?, 1, 0)`,
		taskID, ts, ts, activeMS, activeMS, day)
	if err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
}
