package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Shared ripgrep plumbing for the grep and find tools. Both tools shell out
// to rg when it is available and fall back to a built-in pure-Go
// implementation otherwise, so lcoder stays fully functional on machines
// without rg (at the cost of gitignore awareness and some glob features).

const (
	// searchTimeout bounds any single rg invocation so a runaway search
	// cannot hang the agent loop.
	searchTimeout = 30 * time.Second
	// maxSearchOutputBytes caps captured stdout; the child keeps being
	// drained past the cap so it never blocks on a full pipe.
	maxSearchOutputBytes = 10 << 20 // 10 MiB
	maxSearchErrorBytes  = 4 << 10
)

// rgBinaryPath resolves the rg executable, returning "" when unavailable.
// It is a package-level variable so tests can force the fallback path.
var rgBinaryPath = func() string {
	path, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return path
}

// vcsDirs are version-control metadata directories that search tools always
// exclude, even when searching hidden files.
var vcsDirs = []string{".git", ".svn", ".hg", ".bzr", ".jj", ".sl"}

func isVCSDir(name string) bool {
	for _, d := range vcsDirs {
		if name == d {
			return true
		}
	}
	return false
}

// vcsExcludeArgs excludes VCS metadata directories via gitignore-style
// patterns; a bare `!.git` excludes the directory at any depth.
func vcsExcludeArgs() []string {
	args := make([]string, 0, len(vcsDirs)*2)
	for _, d := range vcsDirs {
		args = append(args, "--glob", "!"+d)
	}
	return args
}

// cappedBuffer discards bytes past its cap while still reporting full writes,
// so a child process producing unbounded output is drained, not blocked.
type cappedBuffer struct {
	buf       bytes.Buffer
	cap       int
	truncated bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := w.cap - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else {
		w.truncated = true
	}
	return len(p), nil
}

// runSearchCommand runs a search subprocess with a timeout and capped,
// drained stdout/stderr. dir pins the child working directory ("" = inherit).
func runSearchCommand(ctx context.Context, dir, name string, args ...string) (stdout, stderr string, stdoutTruncated bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out := &cappedBuffer{cap: maxSearchOutputBytes}
	errOut := &cappedBuffer{cap: maxSearchErrorBytes}
	cmd.Stdout = out
	cmd.Stderr = errOut
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", false, fmt.Errorf("search timed out after %s; narrow the search with a more specific path or glob filter", searchTimeout)
	}
	if ctx.Err() != nil {
		return "", "", false, ctx.Err()
	}
	return out.buf.String(), errOut.buf.String(), out.truncated, runErr
}

// classifySearchError maps an rg failure (exit code 2) to an actionable
// error message based on stderr content.
func classifySearchError(tool, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "glob"):
		return fmt.Errorf("invalid glob pattern: %s", msg)
	case strings.Contains(lower, "regex") || strings.Contains(lower, "syntax") || strings.Contains(lower, "parse error"):
		return fmt.Errorf("invalid regex pattern: %s", msg)
	case strings.Contains(lower, "permission denied"):
		return fmt.Errorf("%s: permission denied: %s", tool, msg)
	case strings.Contains(lower, "no such file"):
		return fmt.Errorf("path not found: %s", msg)
	default:
		return fmt.Errorf("%s failed: %v: %s", tool, err, msg)
	}
}
