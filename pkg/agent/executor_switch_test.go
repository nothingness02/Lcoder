package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
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
