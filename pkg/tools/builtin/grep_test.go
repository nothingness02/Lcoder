package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
