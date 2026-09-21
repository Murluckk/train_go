package runner

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
)

// accumulator turns the `go test -json` stream into a verdict.
//
// Parsing the structured stream rather than grepping for "--- FAIL" is the
// whole reason -json is used: test2json already did this work, and since Go
// 1.24 build failures come through it too.
type accumulator struct {
	mu sync.Mutex

	passed, failed, skipped int
	failedTests             []string

	buildFailed bool
	buildOutput []string
	raw         []string

	sawTestTimeout bool
	sawPanic       bool
	sawTests       bool
}

// maxDiagnosticLines caps what is quoted back in the verdict message. The full
// output is always in the log stream.
const maxDiagnosticLines = 40

func (a *accumulator) note(ev testEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch ev.Action {
	case "build-fail":
		a.buildFailed = true
	case "build-output":
		a.appendBuildLine(ev.Output)
	}

	if ev.Output != "" {
		switch {
		case strings.Contains(ev.Output, "panic: test timed out after"):
			a.sawTestTimeout = true
		case strings.HasPrefix(ev.Output, "panic: "), strings.Contains(ev.Output, "\npanic: "):
			a.sawPanic = true
		}
		// A compile error inside a test-only package is reported as package
		// level output rather than a build-output action.
		if ev.Test == "" && isCompileDiagnostic(ev.Output) {
			a.buildFailed = true
			a.appendBuildLine(ev.Output)
		}
	}

	if ev.Test == "" {
		return
	}
	a.sawTests = true
	// Subtests are recorded by name but not counted, so that "3 passed" means
	// three test functions rather than an arbitrary number of table rows.
	sub := strings.Contains(ev.Test, "/")
	switch ev.Action {
	case "pass":
		if !sub {
			a.passed++
		}
	case "skip":
		if !sub {
			a.skipped++
		}
	case "fail":
		if !sub {
			a.failed++
		}
		a.failedTests = append(a.failedTests, ev.Test)
	}
}

func (a *accumulator) noteRaw(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.raw) < maxDiagnosticLines {
		if s := strings.TrimSpace(line); s != "" {
			a.raw = append(a.raw, s)
		}
	}
	if isCompileDiagnostic(line) {
		a.buildFailed = true
		a.appendBuildLine(line)
	}
}

// appendBuildLine must be called with the mutex held.
func (a *accumulator) appendBuildLine(s string) {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" || len(a.buildOutput) >= maxDiagnosticLines {
		return
	}
	a.buildOutput = append(a.buildOutput, s)
}

// compileMarkers are the phrases the toolchain uses when the code never got as
// far as running.
var compileMarkers = []string{
	"[build failed]",
	"syntax error",
	"undefined:",
	"cannot find package",
	"no required module provides",
	"declared and not used",
	"imported and not used",
	"is not a type",
	"too many return values",
	"not enough arguments",
	"cannot use ",
	"missing return",
	"expected declaration",
}

func isCompileDiagnostic(s string) bool {
	for _, m := range compileMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// result classifies the run. ctx is the run's own context (cancel or
// shutdown), hardCtx additionally carries the outer deadline.
func (a *accumulator) result(ctx, hardCtx context.Context, waitErr error) Result {
	a.mu.Lock()
	defer a.mu.Unlock()

	res := Result{
		Passed:      a.passed,
		Failed:      a.failed,
		Skipped:     a.skipped,
		FailedTests: a.failedTests,
	}

	switch {
	case ctx.Err() != nil:
		res.Outcome = OutcomeCanceled
		res.Message = contextMessage(ctx)
		return res

	case errors.Is(hardCtx.Err(), context.DeadlineExceeded):
		// go test did not even manage to honour its own -timeout, so the
		// process group was killed. This is the wedged-toolchain case, not
		// the ordinary hung-test case below.
		res.Outcome = OutcomeTimeout
		res.Message = "the run exceeded the hard timeout and the whole process group was killed"
		return res

	case a.sawTestTimeout:
		// go test won the race and printed a full goroutine dump, which is
		// precisely what a stuck-goroutine exercise needs to show.
		res.Outcome = OutcomeTimeout
		res.Message = "a test did not finish in time; the goroutine dump is in the output"
		return res

	case a.buildFailed:
		res.Outcome = OutcomeBuildFailed
		res.Message = a.diagnostic("the code did not compile")
		return res

	case waitErr == nil:
		res.Outcome = OutcomePassed
		return res
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		res.Outcome = OutcomeError
		res.Message = waitErr.Error()
		return res
	}

	if !a.sawTests {
		// A non-zero exit with nothing having run means the code never got to
		// the tests: vet, a link error, a package level panic.
		if a.sawPanic {
			res.Outcome = OutcomeFailed
			res.Message = a.diagnostic("the package panicked before any test ran")
			return res
		}
		res.Outcome = OutcomeBuildFailed
		res.Message = a.diagnostic("the tests could not be built or started")
		return res
	}

	res.Outcome = OutcomeFailed
	if a.sawPanic && res.Failed == 0 {
		res.Message = "a test panicked"
	}
	return res
}

// diagnostic must be called with the mutex held.
func (a *accumulator) diagnostic(prefix string) string {
	lines := a.buildOutput
	if len(lines) == 0 {
		lines = a.raw
	}
	if len(lines) == 0 {
		return prefix
	}
	return prefix + ":\n" + strings.Join(lines, "\n")
}
