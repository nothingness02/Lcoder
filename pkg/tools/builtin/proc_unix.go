//go:build !windows

package builtin

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the child in its own process group so killTree can signal
// the whole tree (the shell plus any grandchildren it spawned).
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree SIGKILLs the child's entire process group.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
