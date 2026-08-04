package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

func TestHandleSwitchMode_Valid(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_1",
		Name:      switchModeToolName,
		Arguments: map[string]any{"mode": "code"},
	}
	msg := e.handleSwitchMode(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "code" {
		t.Fatalf("expected mode switched to code, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if trc.IsError {
		t.Fatalf("expected non-error result, got error")
	}
	if !strings.Contains(trc.Text(), "code") {
		t.Fatalf("expected result to mention code, got %q", trc.Text())
	}
}

func TestHandleSwitchMode_Unknown(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_2",
		Name:      switchModeToolName,
		Arguments: map[string]any{"mode": "nonexistent"},
	}
	msg := e.handleSwitchMode(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "plan" {
		t.Fatalf("expected mode unchanged, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if !trc.IsError {
		t.Fatal("expected error result for unknown mode")
	}
}

func TestHandleSwitchMode_MissingArg(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_3",
		Name:      switchModeToolName,
		Arguments: map[string]any{},
	}
	msg := e.handleSwitchMode(context.Background(), 0, models.AgentMessage{}, call)

	if cfg.Mode != "plan" {
		t.Fatalf("expected mode unchanged, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if !trc.IsError {
		t.Fatal("expected error result for missing mode argument")
	}
}

func TestSwitchMode_InterceptsExecution(t *testing.T) {
	bus := events.New()
	cfg := Config{Mode: "plan", ModeManager: NewModeManager()}
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192})
	e := &executor{cfg: &cfg, mgr: mgr, registry: tools.NewRegistry("."), emitter: &eventEmitter{bus: bus}}

	call := models.ToolCallContent{
		ID:        "call_4",
		Name:      switchModeToolName,
		Arguments: map[string]any{"mode": "code"},
	}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, []models.ToolCallContent{call})
	msg := results[0]

	if cfg.Mode != "code" {
		t.Fatalf("expected mode switched to code, got %q", cfg.Mode)
	}
	trc := msg.Content[0].(models.ToolResultContent)
	if trc.IsError {
		t.Fatalf("expected non-error result, got error")
	}
}

func TestBaseToolDefinitions_IncludesSwitchMode(t *testing.T) {
	cfg := Config{Mode: "code", ModeManager: NewModeManager()}
	e := &executor{cfg: &cfg, registry: tools.NewRegistry("."), activeDeferred: make(map[string]bool)}

	defs := e.baseToolDefinitions()
	found := false
	for _, d := range defs {
		if d.Name == switchModeToolName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected baseToolDefinitions to include switch_mode")
	}
}
