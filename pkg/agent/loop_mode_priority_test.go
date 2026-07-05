package agent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func modePriorityAgent(t *testing.T, priority config.ModePromptPriority) *Agent {
	t.Helper()
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192, MaxOutput: 16384},
		contextmgr.WithModePromptPriority(priority),
	)
	mgr.SetSystemPrompt("BASE PROMPT")

	mm := NewModeManager()
	mm.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "MODE PROMPT"}

	ag, err := NewBuilder().
		WithConfig(Config{
			BaseSystemPrompt:  "BASE PROMPT",
			Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
			ToolExecutionMode: models.ExecutionParallel,
			ContextManager:    mgr,
			Mode:              "code",
			ModeManager:       mm,
		}).
		WithGatewayClient(llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))).
		WithRegistry(testRegistry(t.TempDir())).
		WithPermissions(permissions.NewEngine(permissions.DefaultConfig())).
		WithEventBus(events.New()).
		Build()
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	return ag
}

func TestApplyMode_Append(t *testing.T) {
	ag := modePriorityAgent(t, config.ModePromptAppend)
	ag.applyMode()

	if !strings.Contains(ag.mgr.SystemPrompt(), "BASE PROMPT") {
		t.Fatalf("expected base prompt in system prompt, got %q", ag.mgr.SystemPrompt())
	}
	if !strings.Contains(ag.mgr.SystemPrompt(), "MODE PROMPT") {
		t.Fatalf("expected mode prompt in system prompt, got %q", ag.mgr.SystemPrompt())
	}
	if strings.Index(ag.mgr.SystemPrompt(), "MODE PROMPT") <= strings.Index(ag.mgr.SystemPrompt(), "BASE PROMPT") {
		t.Fatalf("expected mode prompt after base prompt, got %q", ag.mgr.SystemPrompt())
	}

	modeBlock, ok := ag.mgr.GetBlock(contextmgr.BlockMode, "mode")
	if !ok {
		t.Fatal("expected mode block for append priority")
	}
	if !strings.Contains(modeBlock.Text(), "MODE PROMPT") {
		t.Fatalf("expected mode block to contain mode prompt, got %q", modeBlock.Text())
	}
}

func TestApplyMode_Prepend(t *testing.T) {
	ag := modePriorityAgent(t, config.ModePromptPrepend)
	ag.applyMode()

	if !strings.HasPrefix(ag.mgr.SystemPrompt(), "# Mode: code") {
		t.Fatalf("expected system prompt to start with mode header, got %q", ag.mgr.SystemPrompt())
	}
	if !strings.Contains(ag.mgr.SystemPrompt(), "BASE PROMPT") {
		t.Fatalf("expected base prompt in system prompt, got %q", ag.mgr.SystemPrompt())
	}
	modeIdx := strings.Index(ag.mgr.SystemPrompt(), "MODE PROMPT")
	baseIdx := strings.Index(ag.mgr.SystemPrompt(), "BASE PROMPT")
	if modeIdx == -1 || baseIdx == -1 || modeIdx >= baseIdx {
		t.Fatalf("expected mode prompt before base prompt, got %q", ag.mgr.SystemPrompt())
	}

	if _, ok := ag.mgr.GetBlock(contextmgr.BlockMode, "mode"); ok {
		t.Fatal("expected no mode block for prepend priority")
	}
}

func TestApplyMode_Replace(t *testing.T) {
	ag := modePriorityAgent(t, config.ModePromptReplace)
	ag.applyMode()

	if ag.mgr.SystemPrompt() == "" {
		t.Fatal("expected non-empty system prompt")
	}
	if strings.Contains(ag.mgr.SystemPrompt(), "BASE PROMPT") {
		t.Fatalf("expected base prompt to be replaced, got %q", ag.mgr.SystemPrompt())
	}
	if !strings.Contains(ag.mgr.SystemPrompt(), "MODE PROMPT") {
		t.Fatalf("expected mode prompt in system prompt, got %q", ag.mgr.SystemPrompt())
	}

	if _, ok := ag.mgr.GetBlock(contextmgr.BlockMode, "mode"); ok {
		t.Fatal("expected no mode block for replace priority")
	}
}

func TestApplyMode_NoModeSystemPrompt(t *testing.T) {
	ag := modePriorityAgent(t, config.ModePromptReplace)
	ag.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code"}
	ag.applyMode()

	if ag.mgr.SystemPrompt() != "BASE PROMPT" {
		t.Fatalf("expected base prompt only, got %q", ag.mgr.SystemPrompt())
	}
	if _, ok := ag.mgr.GetBlock(contextmgr.BlockMode, "mode"); ok {
		t.Fatal("expected no mode block when mode has no system prompt")
	}
}
