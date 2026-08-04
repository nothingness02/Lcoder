package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseMentions(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"check @main.go please", []string{"main.go"}},
		{"@a.go and @pkg/b.go", []string{"a.go", "pkg/b.go"}},
		{"no mentions here", nil},
		{"email like foo@bar is not a mention", nil},
		{"@~/notes.md", []string{"~/notes.md"}},
		{"trailing @", nil},
	}
	for _, c := range cases {
		got := parseMentions(c.text)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseMentions(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestResolveAndValidateMentions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if abs, ok := resolveMention(dir, "main.go"); !ok || abs != filepath.Join(dir, "main.go") {
		t.Fatalf("resolveMention relative = %q, %v", abs, ok)
	}
	if _, ok := resolveMention(dir, "missing.go"); ok {
		t.Fatal("expected missing.go unresolved")
	}
	if abs, ok := resolveMention(dir, "sub"); !ok || abs != filepath.Join(dir, "sub") {
		t.Fatalf("resolveMention directory = %q, %v", abs, ok)
	}
	abs := filepath.Join(dir, "main.go")
	if got, ok := resolveMention(dir, abs); !ok || got != abs {
		t.Fatalf("resolveMention absolute = %q, %v", got, ok)
	}

	missing := validateMentions(dir, "see @main.go and @missing.go")
	if !reflect.DeepEqual(missing, []string{"missing.go"}) {
		t.Fatalf("validateMentions = %v", missing)
	}
	if missing := validateMentions(dir, "check @sub"); len(missing) != 0 {
		t.Fatalf("directory mention should validate, got %v", missing)
	}

	labels := mentionLabels(dir, "see @main.go and @sub and @missing.go")
	if !reflect.DeepEqual(labels, []string{"main.go", "sub"}) {
		t.Fatalf("mentionLabels = %v", labels)
	}
}

func TestExpandHomeMentions(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := expandHomeMentions("read @~/notes.md and @main.go")
	want := "read @" + filepath.ToSlash(filepath.Join(home, "notes.md")) + " and @main.go"
	if got != want {
		t.Fatalf("expandHomeMentions = %q, want %q", got, want)
	}
}
