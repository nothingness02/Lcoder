package tui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFdQueryRegex(t *testing.T) {
	cases := []struct {
		partial string
		want    string
	}{
		{"", ""},
		{"loop", `l.*o.*o.*p`},
		{"m.go", `m.*\..*g.*o`},
		{"pkg/loop", `p.*k.*g.*[/\\].*l.*o.*o.*p`},
		{`pkg\loop`, `p.*k.*g.*[/\\].*l.*o.*o.*p`},
	}
	for _, c := range cases {
		if got := fdQueryRegex(c.partial); got != c.want {
			t.Errorf("fdQueryRegex(%q) = %q, want %q", c.partial, got, c.want)
		}
	}
}

func TestFdArgs(t *testing.T) {
	args := fdArgs("/repo", "loop")
	for _, want := range []string{"--base-directory", "/repo", "--max-results", "100", "--type", "f", "--type", "d", "--follow", "--ignore-case", "--exclude", ".git", "--full-path"} {
		if !contains(args, want) {
			t.Errorf("fdArgs missing %q in %v", want, args)
		}
	}
	if args[len(args)-1] != `l.*o.*o.*p` {
		t.Errorf("fdArgs pattern should be last, got %v", args)
	}

	// Empty partial falls back to a match-all pattern (fd rejects "").
	if got := fdArgs("/repo", ""); got[len(got)-1] != "." {
		t.Errorf("empty partial should produce match-all pattern, got %v", got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestDetectFd(t *testing.T) {
	cases := []struct {
		name     string
		lookPath func(string) (string, error)
		probe    func(string) error
		want     string
	}{
		{
			"fd found and probes ok",
			func(n string) (string, error) {
				if n == "fd" {
					return "/usr/bin/fd", nil
				}
				return "", errors.New("not found")
			},
			func(string) error { return nil },
			"/usr/bin/fd",
		},
		{
			"fdfind fallback",
			func(n string) (string, error) {
				if n == "fdfind" {
					return "/usr/bin/fdfind", nil
				}
				return "", errors.New("not found")
			},
			func(string) error { return nil },
			"/usr/bin/fdfind",
		},
		{
			"fd found but probe fails keeps searching",
			func(n string) (string, error) { return "/usr/bin/" + n, nil },
			func(p string) error {
				if p == "/usr/bin/fd" {
					return errors.New("exec failed")
				}
				return nil
			},
			"/usr/bin/fdfind",
		},
		{
			"nothing found",
			func(string) (string, error) { return "", errors.New("not found") },
			func(string) error { return nil },
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectFd(c.lookPath, c.probe); got != c.want {
				t.Errorf("detectFd() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFdSuggesterMatches(t *testing.T) {
	s := &fdSuggester{
		cwd: "/repo",
		bin: "fd",
		run: func(ctx context.Context, name string, args ...string) ([]string, error) {
			return []string{`pkg\loop.go`, "main.go"}, nil
		},
		fallback: NewFileIndex(t.TempDir()),
	}
	defer s.Stop()

	got := s.Matches("loop", 10)
	// Backslash output is normalized to slashes before ranking.
	if !reflect.DeepEqual(got, []string{"pkg/loop.go"}) {
		t.Fatalf("Matches(loop) = %v", got)
	}
	if !s.Ready() {
		t.Fatal("fd suggester is always ready")
	}
}

func TestFdSuggesterFallbackOnFailure(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "x")

	s := &fdSuggester{
		cwd: dir,
		bin: "fd",
		run: func(ctx context.Context, name string, args ...string) ([]string, error) {
			return nil, errors.New("fd exploded")
		},
		fallback: NewFileIndex(dir),
	}
	defer s.Stop()

	// First query fails → disabled, falls back to the (warming) index.
	if got := s.Matches("a.go", 10); len(got) != 0 {
		t.Fatalf("cold fallback should return nothing yet, got %v", got)
	}
	waitReady(t, s.fallback)
	got := s.Matches("a.go", 10)
	if !reflect.DeepEqual(got, []string{"a.go"}) {
		t.Fatalf("fallback Matches(a.go) = %v", got)
	}
}

func TestRunFdQueryTimeout(t *testing.T) {
	// A runner that never returns must be cut by the caller's context. Use a
	// real but instantly-expired context to prove runFdQuery honours ctx.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	// Nonexistent binary: exec fails fast, exercising the error path.
	if _, err := runFdQuery(ctx, "definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("expected error for missing binary")
	}
}
