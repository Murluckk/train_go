// Package srs holds the spaced repetition policy.
//
// The schedule is deliberately a fixed ladder rather than SM-2. The goal is to
// come back to the same task on different days, not to model a forgetting
// curve, and a predictable ladder is one the user can reason about.
package srs

import (
	"time"

	"drill/internal/store"
)

// Ladder is the gap, in days, before each repetition.
var Ladder = []int{1, 3, 7, 21}

// Advance returns the schedule after a green run.
//
// The last rung repeats forever instead of graduating the task. Graduating
// would remove it from the daily list, which is the opposite of what the
// trainer is for.
func Advance(cur store.Review, now time.Time) store.Review {
	stage := cur.Stage
	if stage < 0 {
		stage = 0
	}
	gap := Ladder[min(stage, len(Ladder)-1)]
	return store.Review{
		Stage: min(stage+1, len(Ladder)-1),
		DueOn: store.AddDays(now, gap),
	}
}

// Mastered reports whether a task has reached the top of the ladder. It only
// affects presentation: mastered tasks keep coming back.
func Mastered(r store.Review) bool {
	return r.Exists && r.Stage >= len(Ladder)-1
}

// IntervalDays is the gap that will follow the next green run at this stage.
func IntervalDays(stage int) int {
	return Ladder[min(max(stage, 0), len(Ladder)-1)]
}
