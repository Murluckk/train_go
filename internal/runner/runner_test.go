package runner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests drive the real go toolchain. That is the point: the whole job of
// this package is getting os/exec, process groups and test2json right, and a
// mock would test the mock.
func newTestRunner(t *testing.T, runTimeout, hardTimeout time.Duration) *Runner {
	t.Helper()
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}

	work := t.TempDir()
	r, err := New(Config{
		// A shared cache across the package's tests: a per-test GOCACHE would
		// rebuild the standard library with -race every single time.
		CacheDir:    sharedCache(t),
		WorkDir:     work,
		RunTimeout:  runTimeout,
		HardTimeout: hardTimeout,
		MaxParallel: 2,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
		if left, _ := filepath.Glob(filepath.Join(work, "drill-run-*")); len(left) != 0 {
			t.Errorf("sandboxes left behind: %v", left)
		}
	})
	return r
}

func sharedCache(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "drill-test-gocache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func run(t *testing.T, r *Runner, spec Spec, wait time.Duration) (Result, string) {
	t.Helper()
	run, err := r.Start(t.Context(), spec.TaskID, 1, spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()

	res, err := run.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v (the run never finished)", err)
	}
	return res, collectLog(run)
}

func collectLog(r *Run) string {
	events, _ := r.Follow(context.Background(), 0)
	var b strings.Builder
	for _, e := range events {
		if e.Kind == KindLog {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

const passingTest = `package solution

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", Add(2, 3))
	}
}

func TestZero(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Fatal("Add(0, 0) should be 0")
	}
}
`

func TestRunPasses(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "add",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Add(a, b int) int { return a + b }\n"},
		Tests:  map[string]string{"solution_test.go": passingTest},
		Race:   true,
	}, 3*time.Minute)

	if res.Outcome != OutcomePassed {
		t.Fatalf("Outcome = %q, want passed. Message: %s\nLog:\n%s", res.Outcome, res.Message, log)
	}
	if res.Passed != 2 || res.Failed != 0 {
		t.Errorf("passed = %d failed = %d, want 2 and 0", res.Passed, res.Failed)
	}
	if !strings.Contains(log, "PASS") && !strings.Contains(log, "ok") {
		t.Errorf("log does not look like go test output:\n%s", log)
	}
}

func TestRunFails(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "add",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Add(a, b int) int { return a - b }\n"},
		Tests:  map[string]string{"solution_test.go": passingTest},
	}, 3*time.Minute)

	if res.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed.\nLog:\n%s", res.Outcome, log)
	}
	if res.Failed != 1 || res.Passed != 1 {
		t.Errorf("passed = %d failed = %d, want 1 and 1", res.Passed, res.Failed)
	}
	if len(res.FailedTests) != 1 || res.FailedTests[0] != "TestAdd" {
		t.Errorf("FailedTests = %v, want [TestAdd]", res.FailedTests)
	}
	if !strings.Contains(log, "want 5") {
		t.Errorf("the test's own failure message is missing from the log:\n%s", log)
	}
}

func TestRunDoesNotCompile(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "add",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Add(a, b int) int { return a + }\n"},
		Tests:  map[string]string{"solution_test.go": passingTest},
	}, 3*time.Minute)

	if res.Outcome != OutcomeBuildFailed {
		t.Fatalf("Outcome = %q, want build_failed.\nMessage: %s\nLog:\n%s", res.Outcome, res.Message, log)
	}
	if res.Message == "" {
		t.Error("a build failure must explain itself")
	}
	if !strings.Contains(log, "solution.go") {
		t.Errorf("the compiler diagnostic is missing from the log:\n%s", log)
	}
}

func TestRunUndefinedSymbol(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "add",
		Files:  map[string]string{"solution.go": "package solution\n"},
		Tests:  map[string]string{"solution_test.go": passingTest},
	}, 3*time.Minute)

	if res.Outcome != OutcomeBuildFailed {
		t.Fatalf("Outcome = %q, want build_failed for a missing function.\nMessage: %s\nLog:\n%s",
			res.Outcome, res.Message, log)
	}
	if !strings.Contains(res.Message, "Add") {
		t.Errorf("Message = %q, want it to name the undefined symbol", res.Message)
	}
}

func TestRunInfiniteLoop(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 5*time.Second, 60*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "loop",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Spin() { for {} }\n"},
		Tests: map[string]string{"solution_test.go": `package solution

import "testing"

func TestSpin(t *testing.T) { Spin() }
`},
	}, 3*time.Minute)

	if res.Outcome != OutcomeTimeout {
		t.Fatalf("Outcome = %q, want timeout.\nMessage: %s\nLog:\n%s", res.Outcome, res.Message, log)
	}
	if !strings.Contains(log, "test timed out") {
		t.Errorf("go test should have reported its own timeout:\n%s", log)
	}
}

func TestRunStuckGoroutine(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 5*time.Second, 60*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "stuck",
		Files: map[string]string{"solution.go": `package solution

import "sync"

// Wait forgets to call Done, so the WaitGroup never fires.
func Wait() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {}()
	wg.Wait()
}
`},
		Tests: map[string]string{"solution_test.go": `package solution

import "testing"

func TestWait(t *testing.T) { Wait() }
`},
	}, 3*time.Minute)

	if res.Outcome != OutcomeTimeout {
		t.Fatalf("Outcome = %q, want timeout.\nLog:\n%s", res.Outcome, log)
	}
	// The goroutine dump is the entire diagnostic value of letting go test
	// hit its own -timeout rather than hard killing it.
	if !strings.Contains(log, "goroutine ") || !strings.Contains(log, "sync.(*WaitGroup).Wait") {
		t.Errorf("the goroutine dump is missing from the log:\n%s", log)
	}
}

func TestRunPanics(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "boom",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Boom() int { var p *int; return *p }\n"},
		Tests: map[string]string{"solution_test.go": `package solution

import "testing"

func TestBoom(t *testing.T) { Boom() }
`},
	}, 3*time.Minute)

	if res.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed for a panicking test.\nMessage: %s\nLog:\n%s",
			res.Outcome, res.Message, log)
	}
	if !strings.Contains(log, "nil pointer dereference") {
		t.Errorf("the panic message is missing from the log:\n%s", log)
	}
}

func TestRunDetectsDataRace(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 30*time.Second, 120*time.Second)

	res, log := run(t, r, Spec{
		TaskID: "racy",
		Files: map[string]string{"solution.go": `package solution

import "sync"

func Count(n int) int {
	total := 0
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			total++
		}()
	}
	wg.Wait()
	return total
}
`},
		Tests: map[string]string{"solution_test.go": `package solution

import "testing"

func TestCount(t *testing.T) {
	if got := Count(50); got != 50 {
		t.Fatalf("Count(50) = %d, want 50", got)
	}
}
`},
		Race: true,
	}, 4*time.Minute)

	if res.Outcome != OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed; -race must catch this.\nLog:\n%s", res.Outcome, log)
	}
	if !strings.Contains(log, "DATA RACE") {
		t.Errorf("the race detector did not report:\n%s", log)
	}
}

func TestRunCancel(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 60*time.Second, 120*time.Second)

	started, err := r.Start(t.Context(), "loop", 1, Spec{
		TaskID: "loop",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Spin() { for {} }\n"},
		Tests: map[string]string{"solution_test.go": `package solution

import "testing"

func TestSpin(t *testing.T) { Spin() }
`},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait until the test binary is actually running, then pull the plug.
	deadline := time.Now().Add(2 * time.Minute)
	for !strings.Contains(collectLog(started), "RUN") && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	started.Cancel()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, err := started.Wait(ctx)
	if err != nil {
		t.Fatalf("a canceled run never finished: %v", err)
	}
	if res.Outcome != OutcomeCanceled {
		t.Errorf("Outcome = %q, want canceled", res.Outcome)
	}
}

func TestRunRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	tests := []struct {
		name string
		path string
	}{
		{name: "traversal", path: "../escape.go"},
		{name: "absolute", path: "/tmp/escape.go"},
		{name: "go.mod", path: "go.mod"},
		{name: "test file", path: "sneaky_test.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := run(t, r, Spec{
				TaskID: "add",
				Files:  map[string]string{tt.path: "package solution\n"},
				Tests:  map[string]string{"solution_test.go": passingTest},
			}, time.Minute)
			if res.Outcome != OutcomeError {
				t.Errorf("Outcome = %q, want error for path %q", res.Outcome, tt.path)
			}
		})
	}
}

func TestRunRejectsEmptySubmission(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)
	if _, err := r.Start(t.Context(), "add", 1, Spec{TaskID: "add"}); err == nil {
		t.Error("Start() with no files should fail")
	}
}

func TestFollowReplaysFromTheBeginning(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)

	started, err := r.Start(t.Context(), "add", 1, Spec{
		TaskID: "add",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Add(a, b int) int { return a + b }\n"},
		Tests:  map[string]string{"solution_test.go": passingTest},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if _, err := started.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	// A client connecting after the run finished must still get everything.
	all, done := started.Follow(t.Context(), 0)
	if !done {
		t.Error("Follow() should report a finished run as done")
	}
	if len(all) == 0 {
		t.Fatal("Follow(0) returned nothing for a finished run")
	}
	if all[len(all)-1].Kind != KindDone {
		t.Errorf("last event kind = %q, want done", all[len(all)-1].Kind)
	}
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has Seq %d; sequence numbers must be contiguous for Last-Event-ID", i, e.Seq)
		}
	}

	// Reconnecting from the middle returns only the tail.
	mid := all[len(all)/2].Seq
	tail, _ := started.Follow(t.Context(), mid)
	if len(tail) != len(all)-int(mid) {
		t.Errorf("Follow(%d) returned %d events, want %d", mid, len(tail), len(all)-int(mid))
	}
	if len(tail) > 0 && tail[0].Seq != mid+1 {
		t.Errorf("Follow(%d) restarted at %d", mid, tail[0].Seq)
	}
}

func TestShutdownStopsLiveRuns(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 60*time.Second, 120*time.Second)

	started, err := r.Start(t.Context(), "loop", 1, Spec{
		TaskID: "loop",
		Files:  map[string]string{"solution.go": "package solution\n\nfunc Spin() { for {} }\n"},
		Tests: map[string]string{"solution_test.go": `package solution

import "testing"

func TestSpin(t *testing.T) { Spin() }
`},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if !started.Finished() {
		t.Error("Shutdown() returned while a run was still going")
	}
}

func TestGoVersionIsUsable(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, 20*time.Second, 90*time.Second)
	v := r.GoVersion()
	if v == "" || strings.HasPrefix(v, "go") {
		t.Errorf("GoVersion() = %q, want a bare version like 1.26.0", v)
	}
}
