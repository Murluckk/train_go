//go:build !unix

package runner

import "os/exec"

// isolateProcessGroup is a no-op where process groups are not available.
func isolateProcessGroup(*exec.Cmd) {}

// killProcessGroup falls back to killing the direct child. On Windows this
// leaks the compiled test binary for a submission that never terminates;
// wiring up a job object would be the fix if the trainer ever runs there.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
