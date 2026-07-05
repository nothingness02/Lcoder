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
