package main

import (
	"context"
	"os"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestCliConfirmParsesYesNo(t *testing.T) {
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "ls"}},
	}

	runConfirm := func(input string) bool {
		f, err := os.CreateTemp("", "cli-confirm-*.txt")
		if err != nil {
			t.Fatalf("create temp: %v", err)
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(input); err != nil {
			t.Fatalf("write temp: %v", err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatalf("seek temp: %v", err)
		}

		oldStdin := os.Stdin
		os.Stdin = f
		defer func() { os.Stdin = oldStdin }()

		allowed, err := cliConfirm{}.Confirm(context.Background(), info)
		if err != nil {
			t.Fatalf("confirm: %v", err)
		}
		return allowed
	}

	if !runConfirm("y\n") {
		t.Fatal("expected 'y' to allow")
	}
	if runConfirm("n\n") {
		t.Fatal("expected 'n' to deny")
	}
	if runConfirm("\n") {
		t.Fatal("expected empty input to deny")
	}
}

func TestCatalogOverridesFromConfigIncludesMaxOutput(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Catalog = config.ModelCatalog{Models: []config.ModelMeta{{
		ID:            "local-model",
		Provider:      "test",
		ContextWindow: 100000,
		Capabilities:  []string{"tools"},
		Budget:        config.ModelBudget{MaxOutput: 32000},
	}}}

	overrides := catalogOverridesFromConfig(cfg)
	if len(overrides) != 1 {
		t.Fatalf("expected 1 override, got %d", len(overrides))
	}
	if overrides[0].MaxOutput != 32000 {
		t.Fatalf("expected override MaxOutput 32000, got %d", overrides[0].MaxOutput)
	}
}

// TestPrepareAgentWiresModeManager verifies that prepareAgent populates
// setup.cfg.modeManager so the TUI can list and switch modes.
func TestPrepareAgentWiresModeManager(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-wiring-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Use a built-in provider/model and a fake key so provider setup does not
	// block wiring.
	cfg := config.DefaultConfig()
	t.Setenv("OPENAI_API_KEY", "sk-test")

	setup, err := prepareAgent(cfg, dir)
	if err != nil {
		t.Fatalf("prepareAgent: %v", err)
	}
	defer setup.cleanup()

	if setup.cfg.modeManager == nil {
		t.Fatal("expected setup.cfg.modeManager to be wired")
	}
	if len(setup.cfg.modeManager.List()) == 0 {
		t.Fatal("expected at least one default mode to be loaded")
	}
}
