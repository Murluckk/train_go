package runner

import (
	"context"
	"sync"
	"time"
)

// Run is a single execution of a submission. It owns the event buffer that
// clients follow over SSE.
//
// Events are kept in full for the lifetime of the run rather than streamed
// straight to a socket: the browser opens the stream with a second request, a
// page reload reconnects, and neither should lose the beginning of the output.
type Run struct {
	ID        string
	TaskID    string
	AttemptID int64
	StartedAt time.Time

	cancel context.CancelCauseFunc

	mu       sync.Mutex
	events   []Event
	finished bool
	result   Result
	endedAt  time.Time
	// updated is closed and replaced whenever events are appended, which wakes
	// every follower at once without keeping per-subscriber channels.
	updated chan struct{}
}

func newRun(id, taskID string, attemptID int64, startedAt time.Time, cancel context.CancelCauseFunc) *Run {
	return &Run{
		ID:        id,
		TaskID:    taskID,
		AttemptID: attemptID,
		StartedAt: startedAt,
		cancel:    cancel,
		updated:   make(chan struct{}),
	}
}

// maxEvents caps the buffer so that a submission printing in a tight loop
// cannot exhaust memory. Older log lines are dropped; the verdict never is.
const maxEvents = 20_000

func (r *Run) append(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	e.Seq = int64(len(r.events)) + 1
	r.events = append(r.events, e)
	if len(r.events) > maxEvents {
		// Keep the tail: the end of a runaway log is the useful part.
		r.events = append(r.events[:0], r.events[len(r.events)-maxEvents:]...)
	}
	r.wake()
}

func (r *Run) finish(res Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	res.DurationMS = time.Since(r.StartedAt).Milliseconds()
	r.result = res
	r.events = append(r.events, Event{
		Seq:    int64(len(r.events)) + 1,
		Kind:   KindDone,
		Result: &res,
	})
	r.finished = true
	r.endedAt = time.Now()
	r.wake()
}

// wake must be called with the mutex held.
func (r *Run) wake() {
	close(r.updated)
	r.updated = make(chan struct{})
}

// Cancel stops the run. The process group is killed through the command's
// Cancel hook.
func (r *Run) Cancel() {
	r.cancel(errCanceledByUser)
}

// Finished reports whether the run has produced its verdict.
func (r *Run) Finished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

// Result returns the verdict; the boolean is false while the run is going.
func (r *Run) Result() (Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.finished
}

// Follow returns the events after seq. If there are none yet and the run is
// still going, it blocks until there are, the run ends, or ctx is done. The
// boolean reports that everything has now been delivered.
func (r *Run) Follow(ctx context.Context, after int64) (events []Event, done bool) {
	for {
		r.mu.Lock()
		if after < 0 {
			after = 0
		}
		if len(r.events) > 0 && after < r.events[len(r.events)-1].Seq {
			// Sequence numbers stay contiguous across truncation, so the
			// index is arithmetic rather than a scan: a live follower calls
			// this once per batch and must not pay O(n) each time.
			start := max(int(after-r.events[0].Seq+1), 0)
			out := make([]Event, len(r.events)-start)
			copy(out, r.events[start:])
			finished := r.finished
			r.mu.Unlock()
			return out, finished
		}
		if r.finished {
			r.mu.Unlock()
			return nil, true
		}
		wait := r.updated
		r.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// Wait blocks until the run finishes and returns its verdict.
func (r *Run) Wait(ctx context.Context) (Result, error) {
	for {
		r.mu.Lock()
		if r.finished {
			res := r.result
			r.mu.Unlock()
			return res, nil
		}
		wait := r.updated
		r.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
}
