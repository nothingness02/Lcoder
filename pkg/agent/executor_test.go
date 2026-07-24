package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// echoTool records the args it was executed with so tests can observe what the
// executor actually dispatched.
type echoTool struct {
	gotArgs map[string]any
}

func (e *echoTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "echo",
		Description: "Echoes its arguments.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"command": map[string]any{"type": "string"}},
		},
	}
}

func (e *echoTool) Execute(_ context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	e.gotArgs = args
	return models.NewToolExecutionResultText("ok"), nil
}

func TestExecutorBeforeHookModifiedArgs(t *testing.T) {
	echo := &echoTool{}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register("echo", echo)

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "echo", Arguments: map[string]any{"command": "original"},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		BeforeToolCall: func(_ context.Context, _ ToolCallInfo) (*BeforeToolCallResult, error) {
			return &BeforeToolCallResult{ModifiedArgs: map[string]any{"command": "rewritten"}}, nil
		},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, registry, permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if echo.gotArgs["command"] != "rewritten" {
		t.Fatalf("args = %v, hook modification not applied", echo.gotArgs)
	}
}

func TestExecutorBeforeHookNilResult(t *testing.T) {
	echo := &echoTool{}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register("echo", echo)

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "echo", Arguments: map[string]any{"command": "original"},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		BeforeToolCall: func(_ context.Context, _ ToolCallInfo) (*BeforeToolCallResult, error) {
			return nil, nil
		},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, registry, permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if echo.gotArgs["command"] != "original" {
		t.Fatalf("args = %v, want original args untouched by no-op hook", echo.gotArgs)
	}
}

func TestExecutorBeforeHookBlockWithModifiedArgs(t *testing.T) {
	echo := &echoTool{}
	registry := tools.NewRegistry(t.TempDir())
	registry.Register("echo", echo)

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "echo", Arguments: map[string]any{"command": "original"},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionParallel,
		BeforeToolCall: func(_ context.Context, _ ToolCallInfo) (*BeforeToolCallResult, error) {
			return &BeforeToolCallResult{
				Block:        true,
				Reason:       "blocked by test hook",
				ModifiedArgs: map[string]any{"command": "rewritten"},
			}, nil
		},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, registry, permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if echo.gotArgs != nil {
		t.Fatalf("tool executed with args %v despite hook block", echo.gotArgs)
	}

	var blockSurfaced bool
	for _, m := range ag.AllMessages() {
		if m.Role != models.RoleToolResult {
			continue
		}
		tr, ok := m.Content[0].(models.ToolResultContent)
		if !ok {
			continue
		}
		if tr.ToolCallID == "call_1" && tr.IsError && strings.Contains(tr.Text(), "blocked by test hook") {
			blockSurfaced = true
		}
	}
	if !blockSurfaced {
		t.Fatal("expected block reason to surface in the tool result")
	}
}

// Regression: the dedup lookup must run after the before-hook, keyed on the
// final args. Previously the lookup ran first on the original args while the
// store used the post-hook args, so a rewritten call could poison the cache
// for a later genuine call and bypass the hook.
func TestExecutorDedupKeyedOnPostHookArgs(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"x.txt": "content-of-x",
		"y.txt": "content-of-y",
		"z.txt": "content-of-z",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	toolMsg := models.NewAgentMessage(models.RoleAssistant,
		models.ToolCallContent{Type: "tool_call", ID: "call_1", Name: "read", Arguments: map[string]any{"path": "x.txt"}},
		models.ToolCallContent{Type: "tool_call", ID: "call_2", Name: "read", Arguments: map[string]any{"path": "y.txt"}},
	)
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	ag := New(Config{
		SystemPrompt:      "x",
		Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ToolExecutionMode: models.ExecutionSequential,
		BeforeToolCall: func(_ context.Context, info ToolCallInfo) (*BeforeToolCallResult, error) {
			// call_1: x -> y (lands in call_2's original key space).
			// call_2: y -> z (must still execute, not serve call_1's cache).
			rewrites := map[string]string{"x.txt": "y.txt", "y.txt": "z.txt"}
			path, _ := info.Args["path"].(string)
			if dst, ok := rewrites[path]; ok {
				return &BeforeToolCallResult{ModifiedArgs: map[string]any{"path": dst}}, nil
			}
			return nil, nil
		},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) {
			return true, nil
		},
	}, client, testRegistry(root), permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	texts := make(map[string]string)
	dedup := make(map[string]bool)
	for _, m := range ag.AllMessages() {
		if m.Role != models.RoleToolResult {
			continue
		}
		tr, ok := m.Content[0].(models.ToolResultContent)
		if !ok {
			continue
		}
		texts[tr.ToolCallID] = tr.Text()
		if tr.Details != nil {
			dedup[tr.ToolCallID], _ = tr.Details["deduplicated"].(bool)
		}
	}

	if !strings.Contains(texts["call_1"], "content-of-y") {
		t.Fatalf("call_1 should read rewritten path y.txt, got %q", texts["call_1"])
	}
	if !strings.Contains(texts["call_2"], "content-of-z") {
		t.Fatalf("call_2 should execute against z.txt, got poisoned/stale content %q", texts["call_2"])
	}
	if dedup["call_2"] {
		t.Fatal("call_2 must not be served from call_1's dedup cache")
	}
}
