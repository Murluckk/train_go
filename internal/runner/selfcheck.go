package runner

import (
	"context"
	"fmt"
	"time"
)

// SelfCheck compiles and runs a trivial package through the real pipeline.
//
// It exists because the ways the sandbox can be broken - a go.mod declaring a
// version the toolchain refuses, a build cache that is not writable, GOPROXY
// blocking a toolchain switch - all look identical from the outside: every
// submission fails to build, and the user blames their own code. One second
// at startup buys a precise error message instead.
func (r *Runner) SelfCheck(ctx context.Context) error {
	spec := Spec{
		TaskID: "selfcheck",
		Files:  map[string]string{"selfcheck.go": "package selfcheck\n\nfunc Answer() int { return 42 }\n"},
		Tests: map[string]string{"selfcheck_test.go": `package selfcheck

import "testing"

func TestAnswer(t *testing.T) {
	if Answer() != 42 {
		t.Fatal("the sandbox is not running the code it was given")
	}
}
`},
		// -race would double the first-run cost for no extra signal here.
		Race: false,
	}

	run, err := r.Start(ctx, "selfcheck", 0, spec)
	if err != nil {
		return fmt.Errorf("runner self-check: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, r.cfg.HardTimeout+30*time.Second)
	defer cancel()

	res, err := run.Wait(waitCtx)
	if err != nil {
		return fmt.Errorf("runner self-check did not finish: %w", err)
	}
	if res.Outcome != OutcomePassed {
		msg := res.Message
		if msg == "" {
			msg = string(res.Outcome)
		}
		return fmt.Errorf("runner self-check failed (%s): %s\n"+
			"the sandbox cannot build and run Go code, so no submission will ever pass",
			res.Outcome, msg)
	}

	r.mu.Lock()
	delete(r.runs, run.ID)
	r.mu.Unlock()
	return nil
}
