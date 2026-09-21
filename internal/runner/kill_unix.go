//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the command in its own process group.
//
// This is the whole reason a submission with an infinite loop can be stopped.
// "go test" spawns a separate compiled test binary; killing only the direct
// child leaves that binary spinning forever, holding a CPU and the pipe the
// parent is still reading from.
func isolateProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the whole group. The negative pid is what
// makes it a group signal.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone, or setpgid may not have taken
		// effect; fall back to the direct child.
		return cmd.Process.Kill()
	}
	return nil
}
