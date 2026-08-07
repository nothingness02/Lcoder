package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitOutput runs git in dir and returns trimmed stdout. dir "" = inherit the
// process cwd. Errors carry the command and stderr for actionable feedback.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitMainTree returns the main working tree of the repository containing cwd
// (the first `worktree <path>` entry of `git worktree list`), so worktrees are
// always placed under the main tree regardless of where lcoder was launched.
func gitMainTree(cwd string) (string, error) {
	out, err := gitOutput(cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "worktree ")), nil
		}
	}
	return "", fmt.Errorf("git worktree list returned no worktree entry")
}

// currentGitBranch returns the branch checked out at cwd ("HEAD" for a
// detached checkout — callers treat it as unusable for a worktree name).
func currentGitBranch(cwd string) (string, error) {
	b, err := gitOutput(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return b, nil
}

// ensureWorktree prepares the worktree for branch and returns its path. The
// worktree lives at <main-tree>/.lcoder/worktrees/<branch>/; an existing
// directory is reused as-is (idempotent).
//
// Bare "auto" resolves the current branch name and creates a DETACHED
// worktree at HEAD under that name — the current branch is always checked out
// in the main tree, so a detached copy is the only non-conflicting way to
// "start a worktree per the current branch's name". An explicit branch
// attaches it (creating it at HEAD when missing) and refuses branches that are
// already checked out.
//
// The git repository's main tree is the anchor (via `git worktree list`), so
// launching from a worktree still places new worktrees under the main tree.
func ensureWorktree(cwd, branch string) (string, error) {
	main, err := gitMainTree(cwd)
	if err != nil {
		return "", fmt.Errorf("worktree: %w (not a git repository?)", err)
	}

	detached := false
	name := branch
	if branch == "" || branch == "auto" {
		name, err = currentGitBranch(cwd)
		if err != nil {
			return "", fmt.Errorf("worktree: resolve current branch: %w", err)
		}
		if name == "HEAD" {
			return "", fmt.Errorf("worktree: detached HEAD; check out a branch first")
		}
		detached = true
	}

	path := filepath.Join(main, ".lcoder", "worktrees", name)
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return path, nil // already exists — reuse
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("worktree: create parent: %w", err)
	}

	if detached {
		if _, err := gitOutput(cwd, "worktree", "add", "--detach", path); err != nil {
			return "", fmt.Errorf("worktree: add detached %q: %w", name, err)
		}
		return path, nil
	}

	// Refuse re-checking-out the branch currently checked out in the main tree.
	if cur, err := currentGitBranch(cwd); err == nil && cur == branch {
		return "", fmt.Errorf("worktree: branch %q is already checked out in %s; switch branches, pass a different --worktree, or use bare --worktree for a detached copy", branch, main)
	}

	// Add the worktree: attach an existing branch, or create one at HEAD.
	if _, err := gitOutput(cwd, "rev-parse", "--verify", "--quiet", branch); err == nil {
		_, err = gitOutput(cwd, "worktree", "add", path, branch)
	} else {
		_, err = gitOutput(cwd, "worktree", "add", "-b", branch, path)
	}
	if err != nil {
		return "", fmt.Errorf("worktree: add %q: %w", branch, err)
	}
	return path, nil
}
