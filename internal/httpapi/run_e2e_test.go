package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"drill/internal/runner"
	"drill/internal/store"
	"drill/internal/textdiff"
)

const wrongSum = "package solution\n\nfunc Sum(xs []int) int { return len(xs) }\n"

const rightSum = `package solution

func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
`

// startRun posts a submission and returns the run id.
func (h *harness) startRun(attemptID int64, files map[string]string) string {
	h.t.Helper()
	var started struct {
		RunID string `json:"run_id"`
	}
	h.mustDo(http.MethodPost, "/api/runs",
		map[string]any{"attempt_id": attemptID, "files": files}, &started, http.StatusAccepted)
	if started.RunID == "" {
		h.t.Fatal("the server returned an empty run id")
	}
	return started.RunID
}

// stream follows a run's event stream to completion and returns the events.
func (h *harness) stream(runID string, after string) []sseEvent {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet,
		h.srv.URL+"/api/runs/"+runID+"/events", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("open the event stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		h.t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return readSSE(h.t, resp.Body)
}

func doneResult(t *testing.T, events []sseEvent) runner.Result {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("the stream produced no events")
	}
	last := events[len(events)-1]
	if last.Event != "done" {
		t.Fatalf("last event = %q, want done", last.Event)
	}
	var payload struct {
		Result runner.Result `json:"result"`
	}
	if err := json.Unmarshal([]byte(last.Data), &payload); err != nil {
		t.Fatalf("decode done event %q: %v", last.Data, err)
	}
	return payload.Result
}

func logOf(events []sseEvent) string {
	var b strings.Builder
	for _, e := range events {
		if e.Event != "log" {
			continue
		}
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(e.Data), &payload) == nil {
			b.WriteString(payload.Text)
		}
	}
	return b.String()
}

// TestSolveATaskEndToEnd walks the whole loop: open a task, get it wrong, get
// it right, and land on the review screen with the journal entry saved.
func TestSolveATaskEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	// Red run.
	events := h.stream(h.startRun(attempt.ID, map[string]string{"solution.go": wrongSum}), "")
	if res := doneResult(t, events); res.Outcome != runner.OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed.\nLog:\n%s", res.Outcome, logOf(events))
	}
	if !strings.Contains(logOf(events), "want 6") {
		t.Errorf("the failing assertion is missing from the stream:\n%s", logOf(events))
	}

	// The clock keeps running through a red run.
	h.advance(7 * time.Minute)
	h.mustDo(http.MethodPost, "/api/attempts/"+itoa(attempt.ID)+"/heartbeat",
		map[string]any{"active_ms": 60000}, nil, http.StatusOK)

	// Green run.
	events = h.stream(h.startRun(attempt.ID, map[string]string{"solution.go": rightSum}), "")
	res := doneResult(t, events)
	if res.Outcome != runner.OutcomePassed {
		t.Fatalf("Outcome = %q, want passed.\nLog:\n%s", res.Outcome, logOf(events))
	}
	if res.Passed != 1 {
		t.Errorf("Passed = %d, want 1", res.Passed)
	}

	// The bookkeeping happens after the stream closes, so give it a moment.
	final := waitForStatus(t, h, attempt.ID, store.StatusPassed)
	if final.DurationMS < (7 * time.Minute).Milliseconds() {
		t.Errorf("DurationMS = %d, want the full wall clock", final.DurationMS)
	}
	if final.ActiveMS != 60000 {
		t.Errorf("ActiveMS = %d, want only the reported focused time", final.ActiveMS)
	}
	if final.RunCount != 2 || final.FailCount != 1 {
		t.Errorf("runs = %d fails = %d, want 2 and 1", final.RunCount, final.FailCount)
	}

	// The repetition is scheduled.
	review, err := h.store.Review(t.Context(), "slices-01")
	if err != nil {
		t.Fatal(err)
	}
	if review.DueOn != "2026-03-11" {
		t.Errorf("DueOn = %q, want tomorrow", review.DueOn)
	}

	// The reference solution has unlocked.
	h.mustDo(http.MethodGet, "/api/tasks/slices-01/solution", nil, nil, http.StatusOK)

	// The review screen shows my code against the reference.
	var rev reviewResponse
	h.mustDo(http.MethodGet, "/api/attempts/"+itoa(attempt.ID)+"/review", nil, &rev, http.StatusOK)
	if len(rev.Diffs) != 1 || rev.Diffs[0].Path != "solution.go" {
		t.Fatalf("Diffs = %+v", rev.Diffs)
	}
	if rev.NextDueOn != "2026-03-11" || rev.NextInDays != 1 {
		t.Errorf("next repetition = %s in %d days", rev.NextDueOn, rev.NextInDays)
	}
	if !rev.Diffs[0].Identical {
		t.Logf("diff against the reference:\n%+v", rev.Diffs[0].Rows)
	}

	// And the journal entry sticks.
	var note noteResponse
	h.mustDo(http.MethodPost, "/api/notes",
		map[string]any{"attempt_id": attempt.ID, "body": "сразу написал len вместо суммы"},
		&note, http.StatusOK)

	h.mustDo(http.MethodGet, "/api/attempts/"+itoa(attempt.ID)+"/review", nil, &rev, http.StatusOK)
	if rev.Note != "сразу написал len вместо суммы" {
		t.Errorf("Note = %q, want the saved entry", rev.Note)
	}
}

func TestGofmtDiffAppearsInTheReview(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	// Correct, but formatted by hand and not the way gofmt would.
	sloppy := "package solution\n\nfunc Sum(xs []int)  int  {\n  total:=0\n  for _,x:=range xs { total+=x }\n  return total\n}\n"
	events := h.stream(h.startRun(attempt.ID, map[string]string{"solution.go": sloppy}), "")
	if res := doneResult(t, events); res.Outcome != runner.OutcomePassed {
		t.Fatalf("Outcome = %q, want passed.\nLog:\n%s", res.Outcome, logOf(events))
	}
	waitForStatus(t, h, attempt.ID, store.StatusPassed)

	var rev reviewResponse
	h.mustDo(http.MethodGet, "/api/attempts/"+itoa(attempt.ID)+"/review", nil, &rev, http.StatusOK)
	if len(rev.GofmtDiffs) != 1 {
		t.Fatalf("GofmtDiffs = %+v, want the formatting divergence reported", rev.GofmtDiffs)
	}
	if rev.GofmtDiffs[0].Identical {
		t.Error("the gofmt diff should not be empty for hand-spaced code")
	}
	var changed bool
	for _, row := range rev.GofmtDiffs[0].Rows {
		if row.Op != textdiff.OpEqual {
			changed = true
		}
	}
	if !changed {
		t.Error("the gofmt diff has no changed rows")
	}
}

func TestBuildFailureReachesTheClient(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	events := h.stream(h.startRun(attempt.ID, map[string]string{"solution.go": "package solution\n\nfunc Sum(xs []int) int { return }\n"}), "")
	res := doneResult(t, events)
	if res.Outcome != runner.OutcomeBuildFailed {
		t.Fatalf("Outcome = %q, want build_failed.\nLog:\n%s", res.Outcome, logOf(events))
	}
	if res.Message == "" {
		t.Error("a build failure must carry a message")
	}
	if !strings.Contains(logOf(events), "solution.go") {
		t.Errorf("the compiler diagnostic is missing:\n%s", logOf(events))
	}

	// A red run must not unlock the reference solution.
	h.mustDo(http.MethodGet, "/api/tasks/slices-01/solution", nil, nil, http.StatusForbidden)
}

func TestStreamReplaysFromLastEventID(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	runID := h.startRun(attempt.ID, map[string]string{"solution.go": rightSum})
	all := h.stream(runID, "")
	if len(all) < 3 {
		t.Fatalf("got %d events, want a few more", len(all))
	}

	// Reconnecting mid-stream must not repeat what was already delivered.
	resume := all[1].ID
	tail := h.stream(runID, resume)
	if len(tail) != len(all)-2 {
		t.Errorf("resumed stream has %d events, want %d", len(tail), len(all)-2)
	}
	if len(tail) > 0 && tail[0].ID == resume {
		t.Errorf("the resumed stream repeated event %s", resume)
	}
	if got := doneResult(t, tail); got.Outcome != runner.OutcomePassed {
		t.Errorf("Outcome on the resumed stream = %q", got.Outcome)
	}
}

func TestCancelRun(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	runID := h.startRun(attempt.ID, map[string]string{
		"solution.go": "package solution\n\nfunc Sum(xs []int) int {\n\tfor {\n\t}\n}\n",
	})

	go func() {
		// Give the compile a moment, then pull the plug.
		time.Sleep(3 * time.Second)
		req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/runs/"+runID+"/cancel", nil)
		if err != nil {
			return
		}
		resp, err := h.srv.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	events := h.stream(runID, "")
	if res := doneResult(t, events); res.Outcome != runner.OutcomeCanceled {
		t.Errorf("Outcome = %q, want canceled", res.Outcome)
	}

	// A cancelled run is not an attempt at solving and must not be counted.
	final, err := h.store.Attempt(t.Context(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.RunCount != 0 {
		t.Errorf("RunCount = %d, want 0 for a cancelled run", final.RunCount)
	}
}

// waitForStatus polls the attempt until the background bookkeeping lands.
func waitForStatus(t *testing.T, h *harness, attemptID int64, want string) store.Attempt {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := h.store.Attempt(t.Context(), attemptID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempt status = %q after 30s, want %q", got.Status, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
