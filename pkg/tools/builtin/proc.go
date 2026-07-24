package builtin

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// runShellCommand runs `shell -c command` in cwd, enforcing timeout by killing
// the whole process tree (not just the direct child) so backgrounded
// grandchildren cannot outlive the call. We manage the kill ourselves rather
// than using exec.CommandContext so we can signal the process group and reap
// orphans. A non-nil error means the command never started (e.g. shell not
// found); non-zero exits and timeouts are reported via exitCode/timedOut.
func runShellCommand(ctx context.Context, shell, command, cwd string, env []string, timeout time.Duration) (stdout, stderr string, exitCode int, timedOut bool, err error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(shell, "-c", command)
	cmd.Dir = cwd
	cmd.Env = env
	// After we kill the process, a backgrounded grandchild may still hold the
	// stdout/stderr pipe open, which would block Wait indefinitely. WaitDelay
	// bounds that wait: once the process is gone, Wait aborts the I/O copy
	// shortly after rather than hanging on the orphan's pipe handle.
	cmd.WaitDelay = 100 * time.Millisecond

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	setProcGroup(cmd) // platform-specific (Setpgid on Unix; process group for taskkill /T on Windows)

	if err := cmd.Start(); err != nil {
		return "", "", 0, false, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	select {
	case <-runCtx.Done():
		timedOut = runCtx.Err() == context.DeadlineExceeded
		killTree(cmd)
		<-done // reap
	case runErr = <-done:
	}

	exitCode = 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		exitCode = -1
	}
	return stdoutBuf.String(), stderrBuf.String(), exitCode, timedOut, nil
}
