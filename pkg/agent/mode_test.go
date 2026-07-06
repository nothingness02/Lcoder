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
	if dirs[0] != filepath.Join("/tmp/proj", "configs", "agents") {
		t.Fatalf("unexpected dir: %s", dirs[0])
	}
}
