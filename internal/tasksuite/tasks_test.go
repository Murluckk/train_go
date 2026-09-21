// Package tasksuite validates the shipped task definitions.
//
// A task is only useful if its reference solution passes its own tests and
// its starter does not. Both halves are checked here, against the real
// toolchain, so a broken task is caught by CI rather than in the middle of a
// practice session.
package tasksuite

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"drill/internal/catalog"
	"drill/internal/runner"
)

func tasksDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newRunner(t *testing.T) *runner.Runner {
	t.Helper()
	r, err := runner.New(runner.Config{
		CacheDir:    filepath.Join(os.TempDir(), "drill-test-gocache"),
		WorkDir:     t.TempDir(),
		RunTimeout:  60 * time.Second,
		HardTimeout: 5 * time.Minute,
		MaxParallel: max(2, runtime.NumCPU()),
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = r.Shutdown(ctx)
	})
	return r
}

func execute(t *testing.T, r *runner.Runner, task *catalog.Task, files map[string]string) (runner.Result, string) {
	t.Helper()

	run, err := r.Start(t.Context(), task.ID, 0, runner.Spec{
		TaskID: task.ID,
		Files:  files,
		Tests:  catalog.FileMap(task.Tests),
		Race:   task.Race,
	})
	if err != nil {
		t.Fatalf("%s: start: %v", task.ID, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	res, err := run.Wait(ctx)
	if err != nil {
		t.Fatalf("%s: the run never finished: %v", task.ID, err)
	}

	events, _ := run.Follow(context.Background(), 0)
	var log strings.Builder
	for _, e := range events {
		if e.Kind == runner.KindLog {
			log.WriteString(e.Text)
		}
	}
	return res, log.String()
}

func loadTasks(t *testing.T) []*catalog.Task {
	t.Helper()
	snap, err := catalog.Load(tasksDir(t))
	if err != nil {
		t.Fatalf("load tasks: %v", err)
	}
	if errs := snap.Errors(); len(errs) > 0 {
		t.Fatalf("some task directories are broken: %+v", errs)
	}
	if snap.Len() == 0 {
		t.Fatal("no tasks were loaded")
	}
	return snap.All()
}

func TestShippedTasksAreWellFormed(t *testing.T) {
	t.Parallel()
	tasks := loadTasks(t)

	if len(tasks) < 10 {
		t.Errorf("got %d tasks, want at least the ten starter tasks", len(tasks))
	}

	topics := map[string]bool{}
	for _, task := range tasks {
		topics[task.Topic] = true

		t.Run(task.ID, func(t *testing.T) {
			t.Parallel()
			if task.EstimateMin < 5 || task.EstimateMin > 15 {
				t.Errorf("estimate_minutes = %d, the starter set is meant to be 5..15 minute tasks", task.EstimateMin)
			}
			if len(task.Readme) < 100 {
				t.Errorf("README.md is %d bytes; a task needs a real statement", len(task.Readme))
			}
			// The starter has to be a scaffold, not a solution.
			for _, f := range task.Starter {
				if !strings.Contains(f.Content, "package solution") {
					t.Errorf("starter/%s: every task uses `package solution`", f.Path)
				}
			}
			for _, f := range task.Solution {
				if !strings.Contains(f.Content, "package solution") {
					t.Errorf("solution/%s: every task uses `package solution`", f.Path)
				}
			}
		})
	}

	for _, want := range []string{"slices", "maps", "interfaces", "errors", "concurrency", "context", "generics", "json", "http"} {
		if !topics[want] {
			t.Errorf("no task covers the topic %q", want)
		}
	}
}

// TestReferenceSolutionsPass is the test that matters: every shipped solution
// must go green against its own hidden tests.
func TestReferenceSolutionsPass(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()

	r := newRunner(t)
	for _, task := range loadTasks(t) {
		t.Run(task.ID, func(t *testing.T) {
			t.Parallel()
			res, log := execute(t, r, task, catalog.FileMap(task.Solution))
			if res.Outcome != runner.OutcomePassed {
				t.Fatalf("outcome = %q (%s)\nlog:\n%s", res.Outcome, res.Message, log)
			}
			if res.Passed == 0 {
				t.Errorf("no tests ran; the task's tests/ directory is not exercising the solution")
			}
		})
	}
}

// TestStartersFail is the other half: a starter that already passes is a task
// with nothing to practise.
func TestStartersFail(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs Go code; skipped under -short")
	}
	t.Parallel()

	r := newRunner(t)
	for _, task := range loadTasks(t) {
		t.Run(task.ID, func(t *testing.T) {
			t.Parallel()
			res, log := execute(t, r, task, catalog.FileMap(task.Starter))
			if res.Outcome == runner.OutcomePassed {
				t.Fatalf("the starter already passes; there is nothing to solve\nlog:\n%s", log)
			}
		})
	}
}

// TestSolutionsAreGofmtClean keeps the divergence journal honest: it compares
// the user's code with gofmt, so the reference must not be the thing that is
// misformatted.
func TestSolutionsAreGofmtClean(t *testing.T) {
	t.Parallel()

	for _, task := range loadTasks(t) {
		for _, group := range []struct {
			name  string
			files []catalog.File
		}{
			{name: "starter", files: task.Starter},
			{name: "solution", files: task.Solution},
			{name: "tests", files: task.Tests},
		} {
			for _, f := range group.files {
				if !strings.HasSuffix(f.Path, ".go") {
					continue
				}
				t.Run(task.ID+"/"+group.name+"/"+f.Path, func(t *testing.T) {
					t.Parallel()
					formatted, err := formatSource(f.Content)
					if err != nil {
						t.Fatalf("does not parse: %v", err)
					}
					if formatted != f.Content {
						t.Errorf("not gofmt clean; run gofmt -w on it")
					}
				})
			}
		}
	}
}
