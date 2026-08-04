package tui

import (
	"bufio"
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// fdQueryTimeout bounds a single fd subprocess query; on expiry the suggester
// falls back to the cached FileIndex for the rest of the session.
const fdQueryTimeout = 500 * time.Millisecond

// fdSuggester delegates @-completion to an fd(1) subprocess, which traverses
// large trees far faster than filepath.WalkDir. It answers per query (no
// cache), and after the first failure disables itself and serves from the
// fallback FileIndex instead.
type fdSuggester struct {
	cwd string
	bin string

	// run executes the query; injectable for tests.
	run func(ctx context.Context, name string, args ...string) ([]string, error)

	mu       sync.Mutex
	disabled bool
	fallback *FileIndex
}

// newFdSuggester wraps the fd binary at bin with a FileIndex fallback.
func newFdSuggester(cwd, bin string) *fdSuggester {
	return &fdSuggester{cwd: cwd, bin: bin, run: runFdQuery, fallback: NewFileIndex(cwd)}
}

// EnsureStarted is a no-op: fd answers per query and needs no warm-up.
func (s *fdSuggester) EnsureStarted() {}

// Stop releases the fallback index.
func (s *fdSuggester) Stop() { s.fallback.Stop() }

// Ready is always true: fd has no warm-up phase.
func (s *fdSuggester) Ready() bool { return true }

// Matches runs one fd query for partial and re-ranks the result with the same
// fuzzy matcher the FileIndex uses, so both backends order results alike.
func (s *fdSuggester) Matches(partial string, limit int) []string {
	if s.isDisabled() {
		s.fallback.EnsureStarted()
		return s.fallback.Matches(partial, limit)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fdQueryTimeout)
	defer cancel()
	lines, err := s.run(ctx, s.bin, fdArgs(s.cwd, partial)...)
	if err != nil {
		s.disable()
		s.fallback.EnsureStarted()
		return s.fallback.Matches(partial, limit)
	}
	// fd prints platform separators (backslash on Windows); normalize to
	// forward slashes unconditionally so results match FileIndex shape.
	// filepath.ToSlash is a no-op on Unix, so it cannot be used here — a
	// Windows-style path fed by fd would keep its backslashes.
	for i, ln := range lines {
		lines[i] = strings.ReplaceAll(ln, "\\", "/")
	}
	return fuzzyMatchPaths(lines, partial, limit)
}

func (s *fdSuggester) isDisabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disabled
}

func (s *fdSuggester) disable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled = true
}

// detectFd probes for an fd binary: first "fd", then the Debian/Ubuntu name
// "fdfind". lookPath and probe are injectable for tests.
func detectFd(lookPath func(string) (string, error), probe func(string) error) string {
	for _, name := range []string{"fd", "fdfind"} {
		if path, err := lookPath(name); err == nil {
			if probe(path) == nil {
				return path
			}
		}
	}
	return ""
}

// probeFdVersion checks that the candidate binary actually runs.
func probeFdVersion(path string) error {
	return exec.Command(path, "--version").Run()
}

// fdArgs builds the fd invocation for one completion query (kimi-code's
// argument set: files and directories, since mentions can target either).
func fdArgs(cwd, partial string) []string {
	pattern := fdQueryRegex(partial)
	if pattern == "" {
		pattern = "."
	}
	return []string{
		"--base-directory", cwd,
		"--max-results", "100",
		"--type", "f", "--type", "d",
		"--follow",
		"--ignore-case",
		"--exclude", ".git", "--exclude", ".git/**",
		"--full-path",
		pattern,
	}
}

// fdQueryRegex converts a mention partial into a subsequence regex: every rune
// escaped and joined with .*, path separators matching either slash. This
// mirrors the fuzzy matcher's subsequence semantics so fd prunes the same
// candidates fuzzy would.
func fdQueryRegex(partial string) string {
	var b strings.Builder
	for i, r := range partial {
		if i > 0 {
			b.WriteString(".*")
		}
		if r == '/' || r == '\\' {
			b.WriteString(`[/\\]`)
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// runFdQuery executes fd and returns its raw output lines.
func runFdQuery(ctx context.Context, name string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return lines, nil
}
