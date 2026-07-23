package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModeManagerLoadAndGet(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`name: plan
description: Planning mode
system_prompt: You are a planning assistant.
allowed_tools: ["read", "ls"]
`)
	if err := os.WriteFile(filepath.Join(dir, "plan.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	mm := NewModeManager()
	if err := mm.LoadModes([]string{dir}); err != nil {
		t.Fatal(err)
	}

	// Embedded defaults are preloaded; the custom dir overrides "plan".
	if len(mm.List()) < 2 {
		t.Fatalf("expected embedded defaults plus override, got %d", len(mm.List()))
	}

	mode := mm.Get("plan")
	if mode.Description != "Planning mode" {
		t.Fatalf("expected custom plan override, got %q", mode.Description)
	}

	// Unknown falls back to code.
	mode = mm.Get("unknown")
	if mode.Name != "code" {
		t.Fatalf("expected code fallback, got %s", mode.Name)
	}
}

func TestModeManagerAutoDetect(t *testing.T) {
	mm := NewModeManager()
	mm.modes["plan"] = ModeConfig{Name: "plan"}
	mm.modes["test"] = ModeConfig{Name: "test"}
	mm.modes["review"] = ModeConfig{Name: "review"}
	mm.modes["explore"] = ModeConfig{Name: "explore"}
	mm.modes["code"] = ModeConfig{Name: "code"}

	cases := []struct {
		prompt string
		want   string
	}{
		{"design the architecture", "plan"},
		{"write a unit test", "test"},
		{"review this code", "review"},
		{"find all files", "explore"},
		{"add error handling", "code"},
	}

	for _, c := range cases {
		got := mm.Detect(c.prompt)
		if got != c.want {
			t.Errorf("Detect(%q) = %s, want %s", c.prompt, got, c.want)
		}
	}
}

func TestModeManagerDefaultModeDirs(t *testing.T) {
	dirs := DefaultModeDirs("/tmp/proj")
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(dirs))
	}
	// Lowest precedence first: the project dir comes last so LoadModes lets it
	// shadow the user dir on name conflicts.
	if dirs[1] != filepath.Join("/tmp/proj", ".lcoder", "modes") {
		t.Fatalf("unexpected project dir: %s", dirs[1])
	}
}

// The embedded defaults ship inside the binary, so a fresh install run from
// any directory still has all built-in modes without any filesystem setup.
func TestNewModeManagerLoadsEmbeddedDefaults(t *testing.T) {
	mm := NewModeManager()
	for _, name := range []string{"code", "plan", "test", "review", "explore"} {
		mode := mm.Get(name)
		if mode.Name != name {
			t.Fatalf("embedded mode %q missing, got %q", name, mode.Name)
		}
	}
}

// A user/project dir shadows an embedded default by name, and removing the
// file would fall back to the embedded version.
func TestFilesystemModeOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`name: code
description: Custom code mode
`)
	if err := os.WriteFile(filepath.Join(dir, "code.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	mm := NewModeManager()
	embeddedDesc := mm.Get("code").Description
	if err := mm.LoadModes([]string{dir}); err != nil {
		t.Fatal(err)
	}

	if got := mm.Get("code").Description; got != "Custom code mode" {
		t.Fatalf("expected filesystem override, got %q", got)
	}
	if embeddedDesc == "Custom code mode" {
		t.Fatal("embedded default should exist before the override")
	}
}
