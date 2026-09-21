package srs

import (
	"testing"
	"time"

	"drill/internal/store"
)

func TestAdvance(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)

	tests := []struct {
		name      string
		cur       store.Review
		wantStage int
		wantDue   string
	}{
		{name: "never solved", cur: store.Review{}, wantStage: 1, wantDue: "2026-03-11"},
		{name: "after the first repeat", cur: store.Review{Stage: 1, Exists: true}, wantStage: 2, wantDue: "2026-03-13"},
		{name: "after the second", cur: store.Review{Stage: 2, Exists: true}, wantStage: 3, wantDue: "2026-03-17"},
		{name: "at the top", cur: store.Review{Stage: 3, Exists: true}, wantStage: 3, wantDue: "2026-03-31"},
		{name: "stays at the top", cur: store.Review{Stage: 99, Exists: true}, wantStage: 3, wantDue: "2026-03-31"},
		{name: "negative stage is clamped", cur: store.Review{Stage: -5}, wantStage: 1, wantDue: "2026-03-11"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Advance(tt.cur, now)
			if got.Stage != tt.wantStage {
				t.Errorf("Stage = %d, want %d", got.Stage, tt.wantStage)
			}
			if got.DueOn != tt.wantDue {
				t.Errorf("DueOn = %s, want %s", got.DueOn, tt.wantDue)
			}
		})
	}
}

func TestAdvanceNeverGraduatesATask(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)

	r := store.Review{}
	for range 20 {
		r = Advance(r, now)
		if r.DueOn == "" {
			t.Fatal("a repetition must always be scheduled; the point is to keep coming back")
		}
	}
	if !Mastered(store.Review{Stage: r.Stage, Exists: true}) {
		t.Error("after twenty solves the task should read as mastered")
	}
}

func TestIntervalDays(t *testing.T) {
	t.Parallel()
	for stage, want := range map[int]int{-1: 1, 0: 1, 1: 3, 2: 7, 3: 21, 10: 21} {
		if got := IntervalDays(stage); got != want {
			t.Errorf("IntervalDays(%d) = %d, want %d", stage, got, want)
		}
	}
}

func TestMastered(t *testing.T) {
	t.Parallel()
	if Mastered(store.Review{Stage: 3}) {
		t.Error("a task with no review row cannot be mastered")
	}
	if Mastered(store.Review{Stage: 2, Exists: true}) {
		t.Error("stage 2 is not the top of the ladder")
	}
	if !Mastered(store.Review{Stage: 3, Exists: true}) {
		t.Error("stage 3 is the top of the ladder")
	}
}
