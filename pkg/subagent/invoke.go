package subagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// buildInvocationArgs returns the lcoder CLI arguments for a subagent run.
func buildInvocationArgs(agent Agent, task string, cwd string) []string {
	args := []string{"--json", "-p", task}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	if agent.Provider != "" {
		args = append(args, "--provider", agent.Provider)
	}
	if agent.Mode != "" {
		args = append(args, "--mode", agent.Mode)
	}
	return args
}

// runSubprocess executes lcoder with the given arguments and returns stdout.
func runSubprocess(ctx context.Context, lcoderPath string, args []string, cwd string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, lcoderPath, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("subagent timed out after %v", timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("subagent exited %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("subagent run failed: %w", err)
	}
	return out, nil
}
