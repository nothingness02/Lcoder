//go:build windows

package hooks

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcGroup starts the child in a new process group so taskkill /T can
// target the whole tree.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree asks taskkill to terminate the child's entire process tree.
// /T covers descendants, /F forces. Falls back to direct kill on failure.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
