// Package runner compiles and runs a submission in a throwaway module and
// streams the result.
package runner

import (
	"encoding/json"
	"time"
)

// Outcome is the verdict of a finished run.
type Outcome string

// Every way a run can end. The four interesting failure modes named in the
// design - a compile error, an infinite loop, a stuck goroutine and a panic -
// map onto BuildFailed, Timeout, Timeout and Failed respectively.
const (
	OutcomePassed      Outcome = "passed"
	OutcomeFailed      Outcome = "failed"
	OutcomeBuildFailed Outcome = "build_failed"
	OutcomeTimeout     Outcome = "timeout"
	OutcomeCanceled    Outcome = "canceled"
	OutcomeError       Outcome = "error" // the harness itself broke
)

// Terminal reports whether the outcome ends the run.
func (o Outcome) Terminal() bool { return o != "" }

// EventKind discriminates the SSE event stream.
type EventKind string

const (
	// KindLog carries raw output, exactly as the toolchain produced it.
	KindLog EventKind = "log"
	// KindTest carries a structured test result from go test -json.
	KindTest EventKind = "test"
	// KindDone is the last event of a run.
	KindDone EventKind = "done"
)

// Event is one item in a run's stream. Events are numbered from 1 so a client
// reconnecting with Last-Event-ID can be replayed exactly what it missed.
type Event struct {
	Seq  int64     `json:"seq"`
	Kind EventKind `json:"-"`

	// Log
	Stream string `json:"stream,omitempty"` // "stdout", "stderr" or "harness"
	Text   string `json:"text,omitempty"`

	// Test
	Action  string  `json:"action,omitempty"` // run, pass, fail, skip, output
	Package string  `json:"package,omitempty"`
	Test    string  `json:"test,omitempty"`
	Elapsed float64 `json:"elapsed,omitempty"`

	// Done
	Result *Result `json:"result,omitempty"`
}

// Result summarises a finished run.
type Result struct {
	Outcome    Outcome `json:"outcome"`
	DurationMS int64   `json:"duration_ms"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	Skipped    int     `json:"skipped"`
	// FailedTests lists the tests that went red, so the UI can show them
	// without the user scrolling through the log.
	FailedTests []string `json:"failed_tests,omitempty"`
	// Message explains a non-test failure: a compile error summary, the
	// timeout, or a harness problem.
	Message string `json:"message,omitempty"`
}

// OK reports whether the run counts as a solve.
func (r Result) OK() bool { return r.Outcome == OutcomePassed }

// Data renders the event's JSON payload for SSE.
func (e Event) Data() ([]byte, error) { return json.Marshal(e) }

// testEvent is one line of `go test -json` output.
type testEvent struct {
	Time        time.Time `json:"Time"`
	Action      string    `json:"Action"`
	Package     string    `json:"Package"`
	Test        string    `json:"Test"`
	Elapsed     float64   `json:"Elapsed"`
	Output      string    `json:"Output"`
	ImportPath  string    `json:"ImportPath"`
	FailedBuild string    `json:"FailedBuild"`
}
