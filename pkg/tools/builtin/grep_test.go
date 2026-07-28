package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestGrepRegexPattern(t *testing.T) {
	dir := tempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("func Foo() {}\nfunc Bar() {}\n"), 0o644)

	grep := NewGrep(dir)
	result, err := grep.Execute(context.Background(), "call_1", map[string]any{
		"pattern": `func (Foo|Baz)\(`,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "a.go:1:func Foo() {}") {
		t.Fatalf("expected regex match, got %q", text)
	}
	if strings.Contains(text, "Bar") {
		t.Fatal("regex alternation should not match Bar")
	}
}

func TestGrepInvalidRegexIsActionable(t *testing.T) {
	grep := NewGrep(".")
	_, err := grep.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "func Foo(",
	})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex pattern") {
		t.Fatalf("error should name the problem, got %q", err.Error())
	}
}

func TestGrepInvalidGlobIsActionable(t *testing.T) {
	grep := NewGrep(".")
	_, err := grep.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "x",
		"glob":    "[",
	})
	if err == nil {
		t.Fatal("expected error for invalid glob")
	}
	if !strings.Contains(err.Error(), "invalid glob pattern") {
		t.Fatalf("error should name the problem, got %q", err.Error())
	}
}

func TestFindInvalidGlobIsActionable(t *testing.T) {
	find := NewFind(".")
	_, err := find.Execute(context.Background(), "call_1", map[string]any{
		"pattern": "[",
	})
	if err == nil {
		t.Fatal("expected error for invalid glob")
	}
	if !strings.Contains(err.Error(), "invalid glob pattern") {
		t.Fatalf("error should name the problem, got %q", err.Error())
	}
}

// forceNoRipgrep makes the search tools take the built-in fallback path.
func forceNoRipgrep(t *testing.T) {
	t.Helper()
	orig := rgBinaryPath
	rgBinaryPath = func() string { return "" }
	t.Cleanup(func() { rgBinaryPath = orig })
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if rgBinaryPath() == "" {
		t.Skip("ripgrep not available")
	}
}

// bothBackends runs fn once against ripgrep (skipping when unavailable) and
// once against the built-in fallback.
func bothBackends(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	t.Run("ripgrep", func(t *testing.T) {
		requireRipgrep(t)
		fn(t)
	})
	t.Run("fallback", func(t *testing.T) {
		forceNoRipgrep(t)
		fn(t)
	})
}

func grepText(t *testing.T, grep interface {
	Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error)
}, args map[string]any) string {
	t.Helper()
	result, err := grep.Execute(context.Background(), "call_1", args)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	return result.Content[0].(models.TextContent).Text
}

func TestGrepOutputModes(t *testing.T) {
	bothBackends(t, func(t *testing.T) {
		dir := tempDir(t)
		_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("func Foo() {}\nfunc Bar() {}\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing here\n"), 0o644)

		grep := NewGrep(dir)

		text := grepText(t, grep, map[string]any{"pattern": "func", "output_mode": "files_with_matches"})
		if !strings.Contains(text, "a.go") || strings.Contains(text, "func Foo") {
			t.Fatalf("files_with_matches should list paths only, got %q", text)
		}
		if strings.Contains(text, "b.txt") {
			t.Fatalf("non-matching file listed: %q", text)
		}

		text = grepText(t, grep, map[string]any{"pattern": "func", "output_mode": "count"})
		if !strings.Contains(text, "a.go:2") {
			t.Fatalf("count mode should report per-file counts, got %q", text)
		}
	})
}

func TestGrepIgnoreCase(t *testing.T) {
	bothBackends(t, func(t *testing.T) {
		dir := tempDir(t)
		_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("func Foo() {}\n"), 0o644)

		text := grepText(t, NewGrep(dir), map[string]any{"pattern": "FUNC FOO", "ignore_case": true})
		if !strings.Contains(text, "a.go:1:func Foo() {}") {
			t.Fatalf("ignore_case should match, got %q", text)
		}
	})
}

func TestGrepHeadLimitAndOffset(t *testing.T) {
	bothBackends(t, func(t *testing.T) {
		dir := tempDir(t)
		_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hit 1\nhit 2\nhit 3\nhit 4\n"), 0o644)

		grep := NewGrep(dir)
		text := grepText(t, grep, map[string]any{"pattern": "hit", "head_limit": 2})
		if !strings.Contains(text, "hit 1") || !strings.Contains(text, "hit 2") || strings.Contains(text, "hit 3") {
			t.Fatalf("head_limit should window results, got %q", text)
		}
		if !strings.Contains(text, "offset=2") {
			t.Fatalf("truncation notice should point at the next offset, got %q", text)
		}

		text = grepText(t, grep, map[string]any{"pattern": "hit", "head_limit": 2, "offset": 2})
		if strings.Contains(text, "hit 1") || !strings.Contains(text, "hit 3") || !strings.Contains(text, "hit 4") {
			t.Fatalf("offset should page results, got %q", text)
		}
	})
}

func TestGrepContextRequiresRipgrep(t *testing.T) {
	requireRipgrep(t)
	dir := tempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("before\nmatch\nafter\n"), 0o644)

	text := grepText(t, NewGrep(dir), map[string]any{"pattern": "match", "context": 1})
	if !strings.Contains(text, "before") || !strings.Contains(text, "after") {
		t.Fatalf("context lines should be included, got %q", text)
	}
}

func TestGrepFilesWithMatchesSortedByMTime(t *testing.T) {
	bothBackends(t, func(t *testing.T) {
		dir := tempDir(t)
		oldFile := filepath.Join(dir, "old.txt")
		newFile := filepath.Join(dir, "new.txt")
		_ = os.WriteFile(oldFile, []byte("hit\n"), 0o644)
		_ = os.WriteFile(newFile, []byte("hit\n"), 0o644)
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(oldFile, past, past)

		text := grepText(t, NewGrep(dir), map[string]any{"pattern": "hit", "output_mode": "files_with_matches"})
		if !strings.HasPrefix(text, "new.txt") {
			t.Fatalf("most recently modified file should come first, got %q", text)
		}
	})
}

func TestFindBasic(t *testing.T) {
	bothBackends(t, func(t *testing.T) {
		dir := tempDir(t)
		_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644)
		_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("x"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0o644)

		result, err := NewFind(dir).Execute(context.Background(), "call_1", map[string]any{"pattern": "*.go"})
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		text := result.Content[0].(models.TextContent).Text
		if !strings.Contains(text, "a.go") || !strings.Contains(text, "sub/b.go") {
			t.Fatalf("expected both .go files, got %q", text)
		}
		if strings.Contains(text, "c.txt") {
			t.Fatalf("non-matching file listed: %q", text)
		}
	})
}

func TestFindSkipsVCSDirs(t *testing.T) {
	bothBackends(t, func(t *testing.T) {
		dir := tempDir(t)
		_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644)

		result, err := NewFind(dir).Execute(context.Background(), "call_1", map[string]any{"pattern": "HEAD"})
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		text := result.Content[0].(models.TextContent).Text
		if text != "no files matched" {
			t.Fatalf("VCS directories must be excluded, got %q", text)
		}
	})
}

func TestFindPathGlobWithRipgrep(t *testing.T) {
	requireRipgrep(t)
	dir := tempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("x"), 0o644)

	result, err := NewFind(dir).Execute(context.Background(), "call_1", map[string]any{"pattern": "sub/*.go"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	text := result.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "sub/b.go") || strings.Contains(text, "a.go") {
		t.Fatalf("path glob should match relative paths, got %q", text)
	}
}
