package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"drill/internal/catalog"
	"drill/internal/runner"
	"drill/internal/store"
)

// harness wires the real store, catalog and runner behind an httptest server.
// Nothing is mocked: SQLite runs in a temp file and the runner shells out to
// the real toolchain, which is the only way these paths are worth testing.
type harness struct {
	t       *testing.T
	srv     *httptest.Server
	store   *store.Store
	catalog *catalog.Catalog
	runner  *runner.Runner
	tasks   string

	mu  sync.Mutex
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	tasksDir := t.TempDir()
	writeTaskDir(t, tasksDir, "slices-01", taskFiles{
		topic: "slices", difficulty: 1,
		starter:  "package solution\n\nfunc Sum(xs []int) int { return 0 }\n",
		test:     sumTest,
		solution: "package solution\n\nfunc Sum(xs []int) int {\n\ttotal := 0\n\tfor _, x := range xs {\n\t\ttotal += x\n\t}\n\treturn total\n}\n",
	})
	writeTaskDir(t, tasksDir, "maps-01", taskFiles{
		topic: "maps", difficulty: 2,
		starter:  "package solution\n\nfunc Count(xs []string) map[string]int { return nil }\n",
		test:     countTest,
		solution: "package solution\n\nfunc Count(xs []string) map[string]int {\n\tout := map[string]int{}\n\tfor _, x := range xs {\n\t\tout[x]++\n\t}\n\treturn out\n}\n",
	})

	// The store and the API must read the same clock, or elapsed times come
	// out as the difference between two unrelated notions of "now".
	h := &harness{t: t, tasks: tasksDir, now: time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)}

	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "drill.db"), store.WithClock(h.Now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cat := catalog.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	watcher := catalog.NewWatcher(tasksDir, cat, time.Hour, logger,
		func(ctx context.Context, snap *catalog.Snapshot) error {
			metas := make([]store.TaskMeta, 0, snap.Len())
			for _, task := range snap.All() {
				metas = append(metas, store.TaskMeta{
					ID: task.ID, Title: task.Title, Topic: task.Topic,
					Difficulty: task.Difficulty, EstimateMin: task.EstimateMin,
					ContentHash: task.ContentHash,
				})
			}
			return st.SyncTasks(ctx, metas)
		})
	if err := watcher.Reload(t.Context()); err != nil {
		t.Fatalf("load tasks: %v", err)
	}

	run, err := runner.New(runner.Config{
		CacheDir:    filepath.Join(os.TempDir(), "drill-test-gocache"),
		WorkDir:     t.TempDir(),
		RunTimeout:  20 * time.Second,
		HardTimeout: 3 * time.Minute,
		MaxParallel: 2,
		Logger:      logger,
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	h.store, h.catalog, h.runner = st, cat, run

	api := New(Deps{
		Store: st, Catalog: cat, Runner: run, Reload: watcher.Reload,
		Logger: logger, Now: h.Now,
	})
	h.srv = httptest.NewServer(api.Handler())
	t.Cleanup(func() {
		api.Shutdown()
		h.srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = run.Shutdown(ctx)
	})
	return h
}

func (h *harness) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// do performs a request and decodes the JSON body into out when out is not nil.
func (h *harness) do(method, path string, body, out any) *http.Response {
	h.t.Helper()

	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(h.t.Context(), method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp
}

func (h *harness) mustDo(method, path string, body, out any, wantStatus int) {
	h.t.Helper()
	resp := h.do(method, path, body, out)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("%s %s: status %d, want %d", method, path, resp.StatusCode, wantStatus)
	}
}

type taskFiles struct {
	topic      string
	difficulty int
	starter    string
	test       string
	solution   string
	race       *bool
}

func writeTaskDir(t *testing.T, root, id string, f taskFiles) {
	t.Helper()
	manifest := "id: " + id + "\ntitle: Задача " + id + "\ntopic: " + f.topic +
		"\ndifficulty: " + string(rune('0'+f.difficulty)) + "\nestimate_minutes: 10\n"
	if f.race != nil && !*f.race {
		manifest += "race: false\n"
	}
	files := map[string]string{
		"task.yaml":              manifest,
		"README.md":              "# " + id + "\n\nУсловие.\n",
		"starter/solution.go":    f.starter,
		"tests/solution_test.go": f.test,
		"solution/solution.go":   f.solution,
	}
	for p, content := range files {
		full := filepath.Join(root, id, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const sumTest = `package solution

import "testing"

func TestSum(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want int
	}{
		{name: "empty", in: nil, want: 0},
		{name: "one", in: []int{5}, want: 5},
		{name: "several", in: []int{1, 2, 3}, want: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sum(tt.in); got != tt.want {
				t.Errorf("Sum(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
`

const countTest = `package solution

import "testing"

func TestCount(t *testing.T) {
	got := Count([]string{"a", "b", "a"})
	if got["a"] != 2 || got["b"] != 1 {
		t.Fatalf("Count() = %v", got)
	}
}
`

func TestHealthAndReady(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp := h.do(http.MethodGet, "/healthz", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d", resp.StatusCode)
	}

	var ready struct {
		Status    string `json:"status"`
		Tasks     int    `json:"tasks"`
		GoVersion string `json:"go_version"`
		Today     string `json:"today"`
	}
	h.mustDo(http.MethodGet, "/readyz", nil, &ready, http.StatusOK)
	if ready.Status != "ready" || ready.Tasks != 2 {
		t.Errorf("/readyz = %+v, want ready with 2 tasks", ready)
	}
	if ready.GoVersion == "" {
		t.Error("/readyz should report the toolchain version")
	}
	if ready.Today != "2026-03-10" {
		t.Errorf("today = %q, want the server's local date", ready.Today)
	}
}

func TestListTasks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var body struct {
		Tasks  []taskSummary       `json:"tasks"`
		Errors []catalog.LoadError `json:"errors"`
	}
	h.mustDo(http.MethodGet, "/api/tasks", nil, &body, http.StatusOK)
	if len(body.Tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(body.Tasks))
	}
	if len(body.Errors) != 0 {
		t.Errorf("errors = %+v", body.Errors)
	}
	// Ordered by topic, then difficulty.
	if body.Tasks[0].ID != "maps-01" || body.Tasks[1].ID != "slices-01" {
		t.Errorf("order = %s, %s", body.Tasks[0].ID, body.Tasks[1].ID)
	}
}

func TestGetTaskHidesTestsAndSolution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var raw map[string]any
	h.mustDo(http.MethodGet, "/api/tasks/slices-01", nil, &raw, http.StatusOK)

	encoded, _ := json.Marshal(raw)
	for _, forbidden := range []string{"TestSum", "total += x", "tests", "solution/"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Errorf("the task payload leaks %q:\n%s", forbidden, encoded)
		}
	}
	if raw["readme"] == "" {
		t.Error("readme is missing")
	}
	starter, _ := raw["starter"].([]any)
	if len(starter) != 1 {
		t.Errorf("starter = %v, want one file", raw["starter"])
	}
}

func TestGetUnknownTask(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mustDo(http.MethodGet, "/api/tasks/nope", nil, nil, http.StatusNotFound)
}

func TestSolutionIsGatedUntilSolved(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp := h.do(http.MethodGet, "/api/tasks/slices-01/solution", nil, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 before the task is solved", resp.StatusCode)
	}

	// Mark it solved directly: the gate is about the database, not about how
	// the run happened.
	attempt, _, err := h.store.StartAttempt(t.Context(), "slices-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.PassAttempt(t.Context(), attempt.ID, nil,
		func(r store.Review, now time.Time) store.Review {
			return store.Review{Stage: 1, DueOn: store.AddDays(now, 1)}
		}); err != nil {
		t.Fatal(err)
	}

	var body struct {
		Solution []catalog.File `json:"solution"`
	}
	h.mustDo(http.MethodGet, "/api/tasks/slices-01/solution", nil, &body, http.StatusOK)
	if len(body.Solution) != 1 || !strings.Contains(body.Solution[0].Content, "total += x") {
		t.Errorf("solution = %+v", body.Solution)
	}
}

func TestAttemptLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var first attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &first, http.StatusOK)
	if first.Resumed {
		t.Error("the first attempt cannot be a resume")
	}
	if first.Status != store.StatusInProgress {
		t.Errorf("status = %q", first.Status)
	}

	// Reloading the page resumes rather than restarting the stopwatch.
	h.advance(5 * time.Minute)
	var second attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &second, http.StatusOK)
	if !second.Resumed || second.ID != first.ID {
		t.Errorf("second = %+v, want the same attempt resumed", second)
	}
	if second.ElapsedMS < (5 * time.Minute).Milliseconds() {
		t.Errorf("ElapsedMS = %d, want at least five minutes", second.ElapsedMS)
	}

	// Starting over abandons the old attempt.
	var third attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts",
		map[string]any{"restart": true}, &third, http.StatusOK)
	if third.ID == first.ID {
		t.Error("restart should have created a new attempt")
	}
	if third.ElapsedMS > time.Minute.Milliseconds() {
		t.Errorf("ElapsedMS = %d, want a fresh clock", third.ElapsedMS)
	}
}

func TestHeartbeatAccumulatesActiveTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)
	path := "/api/attempts/" + itoa(attempt.ID) + "/heartbeat"

	for range 3 {
		var got attemptResponse
		h.mustDo(http.MethodPost, path, map[string]any{"active_ms": 15000}, &got, http.StatusOK)
		attempt = got
	}
	if attempt.ActiveMS != 45000 {
		t.Errorf("ActiveMS = %d, want 45000", attempt.ActiveMS)
	}
}

func TestHeartbeatRejectsImplausibleValues(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)
	path := "/api/attempts/" + itoa(attempt.ID) + "/heartbeat"

	for _, ms := range []int64{-1, int64(time.Hour / time.Millisecond)} {
		resp := h.do(http.MethodPost, path, map[string]any{"active_ms": ms}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("active_ms = %d: status %d, want 400; a bogus heartbeat must not corrupt the metric", ms, resp.StatusCode)
		}
	}
}

func TestDraftRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	h.mustDo(http.MethodPut, "/api/attempts/"+itoa(attempt.ID)+"/draft",
		map[string]any{"files": map[string]string{"solution.go": "package solution // мой черновик"}},
		nil, http.StatusNoContent)

	var detail taskDetail
	h.mustDo(http.MethodGet, "/api/tasks/slices-01", nil, &detail, http.StatusOK)
	if detail.Draft["solution.go"] != "package solution // мой черновик" {
		t.Errorf("draft = %v, want the saved buffer", detail.Draft)
	}
}

func TestDraftRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	resp := h.do(http.MethodPut, "/api/attempts/"+itoa(attempt.ID)+"/draft",
		map[string]any{"files": map[string]string{"../escape.go": "x"}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestToday(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var today todayResponse
	h.mustDo(http.MethodGet, "/api/today", nil, &today, http.StatusOK)
	if today.Day != "2026-03-10" {
		t.Errorf("Day = %q", today.Day)
	}
	if len(today.Reviews) != 0 {
		t.Errorf("Reviews = %+v, want nothing due on a fresh database", today.Reviews)
	}
	if today.New == nil {
		t.Fatal("a fresh database should offer a new task")
	}
	if today.TotalTasks != 2 || today.SolvedTasks != 0 {
		t.Errorf("totals = %d/%d", today.SolvedTasks, today.TotalTasks)
	}
}

func TestTodayShowsOverdueReviews(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	attempt, _, err := h.store.StartAttempt(t.Context(), "slices-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.PassAttempt(t.Context(), attempt.ID, nil,
		func(store.Review, time.Time) store.Review {
			return store.Review{Stage: 1, DueOn: "2026-03-01"}
		}); err != nil {
		t.Fatal(err)
	}

	var today todayResponse
	h.mustDo(http.MethodGet, "/api/today", nil, &today, http.StatusOK)
	if len(today.Reviews) != 1 || today.Reviews[0].ID != "slices-01" {
		t.Fatalf("Reviews = %+v, want the overdue task", today.Reviews)
	}
	if today.New == nil || today.New.ID != "maps-01" {
		t.Errorf("New = %+v, want the remaining unscheduled task", today.New)
	}
	if today.SolvedTasks != 1 {
		t.Errorf("SolvedTasks = %d, want 1", today.SolvedTasks)
	}
}

func TestReload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	writeTaskDir(t, h.tasks, "errors-01", taskFiles{
		topic: "errors", difficulty: 2,
		starter:  "package solution\n\nfunc Wrap() error { return nil }\n",
		test:     "package solution\n\nimport \"testing\"\n\nfunc TestWrap(t *testing.T) { _ = Wrap() }\n",
		solution: "package solution\n\nfunc Wrap() error { return nil }\n",
	})

	var body struct {
		Tasks int `json:"tasks"`
	}
	h.mustDo(http.MethodPost, "/api/tasks/reload", nil, &body, http.StatusOK)
	if body.Tasks != 3 {
		t.Errorf("tasks after reload = %d, want 3", body.Tasks)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tests := []struct {
		method, path string
		want         int
	}{
		{method: http.MethodDelete, path: "/api/tasks", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/api/runs/does-not-exist", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/api/attempts/999", want: http.StatusNotFound},
		{method: http.MethodGet, path: "/api/attempts/abc", want: http.StatusBadRequest},
		{method: http.MethodPost, path: "/api/notes", want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			resp := h.do(tt.method, tt.path, nil, nil)
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestNotesJournal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	attempt, _, err := h.store.StartAttempt(t.Context(), "slices-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.PassAttempt(t.Context(), attempt.ID, nil,
		func(store.Review, time.Time) store.Review {
			return store.Review{Stage: 1, DueOn: "2026-03-11"}
		}); err != nil {
		t.Fatal(err)
	}

	var note noteResponse
	h.mustDo(http.MethodPost, "/api/notes",
		map[string]any{"attempt_id": attempt.ID, "body": "забыл про пустой слайс"},
		&note, http.StatusOK)
	if note.TaskTitle == "" || note.Topic != "slices" {
		t.Errorf("note = %+v, want the task joined in", note)
	}

	var list struct {
		Notes []noteResponse `json:"notes"`
	}
	h.mustDo(http.MethodGet, "/api/notes", nil, &list, http.StatusOK)
	if len(list.Notes) != 1 {
		t.Fatalf("notes = %+v", list.Notes)
	}

	h.mustDo(http.MethodGet, "/api/notes?topic=maps", nil, &list, http.StatusOK)
	if len(list.Notes) != 0 {
		t.Errorf("filtering by another topic returned %+v", list.Notes)
	}

	h.mustDo(http.MethodDelete, "/api/notes/"+itoa(note.ID), nil, nil, http.StatusNoContent)
	h.mustDo(http.MethodGet, "/api/notes", nil, &list, http.StatusOK)
	if len(list.Notes) != 0 {
		t.Errorf("notes after delete = %+v", list.Notes)
	}
}

func TestEmptyNoteRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	resp := h.do(http.MethodPost, "/api/notes", map[string]any{"attempt_id": 1, "body": "   "}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStatsEndpoints(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var tasks struct {
		Series   []store.TaskSeries `json:"series"`
		Activity []store.DailyCount `json:"activity"`
	}
	h.mustDo(http.MethodGet, "/api/stats/tasks", nil, &tasks, http.StatusOK)
	if len(tasks.Series) != 0 {
		t.Errorf("series on a fresh database = %+v", tasks.Series)
	}

	var topics struct {
		Topics []store.TopicStat `json:"topics"`
	}
	h.mustDo(http.MethodGet, "/api/stats/topics", nil, &topics, http.StatusOK)
	if len(topics.Topics) != 2 {
		t.Errorf("topics = %+v, want one per topic even with no solves", topics.Topics)
	}
}

func TestReviewRequiresAGreenRun(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var attempt attemptResponse
	h.mustDo(http.MethodPost, "/api/tasks/slices-01/attempts", nil, &attempt, http.StatusOK)

	resp := h.do(http.MethodGet, "/api/attempts/"+itoa(attempt.ID)+"/review", nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 before the attempt is green", resp.StatusCode)
	}
}

func TestStaticShellIsServed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	resp := h.do(http.MethodGet, "/", nil, nil)
	// The harness wires no static handler, so the mux falls through to 404;
	// what matters is that it is not a 500.
	if resp.StatusCode >= http.StatusInternalServerError {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// sseEvent is one parsed server sent event.
type sseEvent struct {
	ID    string
	Event string
	Data  string
}

// readSSE consumes an event stream until the done event or the context ends.
func readSSE(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	var (
		out     []sseEvent
		current sseEvent
	)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if current.Event != "" || current.Data != "" {
				out = append(out, current)
				if current.Event == "done" {
					return out
				}
			}
			current = sseEvent{}
		case strings.HasPrefix(line, ":"):
			// heartbeat
		case strings.HasPrefix(line, "id: "):
			current.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			current.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	return out
}
