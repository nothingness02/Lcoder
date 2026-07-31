package main

import (
	"context"
	"os"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/skills"
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

func TestParseConfirmScope(t *testing.T) {
	cases := []struct {
		input string
		want  agent.ConfirmScope
	}{
		{"y", agent.ScopeOnce},
		{"Y", agent.ScopeOnce},
		{"yes", agent.ScopeOnce},
		{"p", agent.ScopeProject},
		{"project", agent.ScopeProject},
		{"g", agent.ScopeGlobal},
		{"global", agent.ScopeGlobal},
		{"n", agent.ScopeDeny},
		{"N", agent.ScopeDeny},
		{"no", agent.ScopeDeny},
		{"deny", agent.ScopeDeny},
		{"", agent.ScopeDeny},
	}
	for _, c := range cases {
		got, err := parseConfirmScope(c.input)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %v, want %v", c.input, got, c.want)
		}
	}

	if _, err := parseConfirmScope("xyz"); err == nil {
		t.Fatal("expected error for unknown choice")
	}
}

func TestIsUltraDestructiveMatchesRMRF(t *testing.T) {
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "rm -rf /"}},
	}
	if !permissions.IsUltraDestructiveCommand(info.BashCommand()) {
		t.Fatal("expected rm -rf / to be ultra-destructive")
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
func TestParseConfirmScopeSession(t *testing.T) {
	scope, err := parseConfirmScope("s")
	if err != nil || scope != agent.ScopeSession {
		t.Fatalf("s should map to ScopeSession, got %v, %v", scope, err)
	}
	scope, err = parseConfirmScope("session")
	if err != nil || scope != agent.ScopeSession {
		t.Fatalf("session should map to ScopeSession, got %v, %v", scope, err)
	}
}

// 禁用清单的两层(config.yaml 声明 + skills.yaml 持久化)必须取并集:
// config 层在任何情况下都不应被 persisted 层覆盖(含 persisted 为空)。
func TestApplyDisabledLayersUnionsBothSources(t *testing.T) {
	cat := skills.NewCatalog([]skills.ScopedMeta{
		{SkillMeta: skills.SkillMeta{Name: "alpha"}},
		{SkillMeta: skills.SkillMeta{Name: "beta"}},
		{SkillMeta: skills.SkillMeta{Name: "gamma"}},
	})

	applyDisabledLayers(cat, []string{"alpha"}, []string{"beta"})

	if !cat.IsDisabled("alpha") {
		t.Fatal("config-declared disable must apply")
	}
	if !cat.IsDisabled("beta") {
		t.Fatal("persisted disable must apply")
	}
	if cat.IsDisabled("gamma") {
		t.Fatal("unlisted skill must stay enabled")
	}
}

// persisted 为空(文件不存在或空清单)时,config 层的禁用必须保留——
// 这是原 bug:LoadDisabledFile 对缺失文件返回 (nil, nil),SetDisabledAll(nil)
// 会把 config 层清空。
func TestApplyDisabledLayersKeepsConfigWhenPersistedEmpty(t *testing.T) {
	cat := skills.NewCatalog([]skills.ScopedMeta{
		{SkillMeta: skills.SkillMeta{Name: "alpha"}},
	})

	applyDisabledLayers(cat, []string{"alpha"}, nil)

	if !cat.IsDisabled("alpha") {
		t.Fatal("config-declared disable must survive an empty persisted layer")
	}
}
