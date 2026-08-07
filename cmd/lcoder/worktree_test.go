package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo creates a temp git repository with one commit and the named branch
// checked out.
func gitRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@lcoder.dev")
	run("config", "user.name", "lcoder test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "init")
	run("checkout", "-q", "-b", branch)
	return dir
}

func TestEnsureWorktreeAutoUsesCurrentBranch(t *testing.T) {
	repo := gitRepo(t, "feat/auto")
	path, err := ensureWorktree(repo, "auto")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	want := filepath.Join(repo, ".lcoder", "worktrees", "feat/auto")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	// The current branch is checked out in the main tree, so the auto worktree
	// is a DETACHED copy at HEAD named after the branch.
	b, err := gitOutput(path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("read worktree branch: %v", err)
	}
	if b != "HEAD" {
		t.Fatalf("auto worktree should be detached, got branch %q", b)
	}
	head, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	wtHead, err := gitOutput(path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if wtHead != head {
		t.Fatalf("auto worktree HEAD = %s, want %s (current commit)", wtHead, head)
	}
}

func TestEnsureWorktreeExplicitExistingBranch(t *testing.T) {
	repo := gitRepo(t, "main-branch")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-q", "-b", "feat/exists")
	run("checkout", "-q", "main-branch")

	path, err := ensureWorktree(repo, "feat/exists")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	if b, _ := gitOutput(path, "rev-parse", "--abbrev-ref", "HEAD"); b != "feat/exists" {
		t.Fatalf("worktree branch = %q, want feat/exists", b)
	}

	// Idempotent: a second call reuses the existing directory.
	again, err := ensureWorktree(repo, "feat/exists")
	if err != nil || again != path {
		t.Fatalf("second call = %q, %v; want %q", again, err, path)
	}
}

func TestEnsureWorktreeCreatesNewBranch(t *testing.T) {
	repo := gitRepo(t, "base")
	path, err := ensureWorktree(repo, "feat/new")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	b, _ := gitOutput(path, "rev-parse", "--abbrev-ref", "HEAD")
	if b != "feat/new" {
		t.Fatalf("created branch = %q, want feat/new", b)
	}
}

func TestEnsureWorktreeRefusesCurrentBranch(t *testing.T) {
	repo := gitRepo(t, "main-branch")
	if _, err := ensureWorktree(repo, "main-branch"); err == nil {
		t.Fatal("expected an error for re-checking-out the current branch")
	}
}

func TestEnsureWorktreeNotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := ensureWorktree(dir, "auto"); err == nil {
		t.Fatal("expected an error outside a git repository")
	}
}

func TestGitMainTreeFromWorktree(t *testing.T) {
	repo := gitRepo(t, "base")
	wt, err := ensureWorktree(repo, "feat/from-wt")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	// Running from inside the worktree still anchors on the main tree.
	main, err := gitMainTree(wt)
	if err != nil {
		t.Fatalf("gitMainTree: %v", err)
	}
	if filepath.Clean(main) != filepath.Clean(repo) {
		t.Fatalf("main tree = %q, want %q", main, repo)
	}
}
