package hooks

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/models"
)

func testInfo(args map[string]any) agent.ToolCallInfo {
	return agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash", ID: "1"},
		Args:     args,
	}
}

func TestCompositeNilHookPassthrough(t *testing.T) {
	h := CompositeBeforeToolCall(nil, func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return nil, nil
	})
	result, err := h(context.Background(), testInfo(map[string]any{"command": "ls"}))
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
}

func TestCompositeModifiedArgsSurface(t *testing.T) {
	nilHook := func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return nil, nil
	}
	modHook := func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return &agent.BeforeToolCallResult{ModifiedArgs: map[string]any{"command": "rewritten"}}, nil
	}
	h := CompositeBeforeToolCall(nilHook, modHook)
	result, err := h(context.Background(), testInfo(map[string]any{"command": "ls"}))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Block {
		t.Fatalf("expected non-blocking result with ModifiedArgs, got %+v", result)
	}
	if result.ModifiedArgs["command"] != "rewritten" {
		t.Fatalf("expected rewritten args, got %v", result.ModifiedArgs)
	}
}

func TestCompositeLaterHookSeesModifiedArgs(t *testing.T) {
	var seen map[string]any
	first := func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return &agent.BeforeToolCallResult{ModifiedArgs: map[string]any{"command": "rewritten"}}, nil
	}
	second := func(_ context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		seen = info.Args
		return nil, nil
	}
	h := CompositeBeforeToolCall(first, second)
	if _, err := h(context.Background(), testInfo(map[string]any{"command": "ls"})); err != nil {
		t.Fatal(err)
	}
	if seen["command"] != "rewritten" {
		t.Fatalf("later hook saw %v, want rewritten args", seen)
	}
}

func TestCompositeBlockWins(t *testing.T) {
	modHook := func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return &agent.BeforeToolCallResult{ModifiedArgs: map[string]any{"command": "rewritten"}}, nil
	}
	blockHook := func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		return &agent.BeforeToolCallResult{Block: true, Reason: "denied"}, nil
	}
	ran := false
	afterBlock := func(context.Context, agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		ran = true
		return nil, nil
	}
	h := CompositeBeforeToolCall(modHook, blockHook, afterBlock)
	result, err := h(context.Background(), testInfo(map[string]any{"command": "ls"}))
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Block || result.Reason != "denied" {
		t.Fatalf("expected block result, got %+v", result)
	}
	if ran {
		t.Fatal("hook after a blocking hook must not run")
	}
}
