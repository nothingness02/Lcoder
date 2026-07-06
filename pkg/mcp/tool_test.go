package mcp

import (
	"context"
	"testing"
	"time"
)

func TestExecutableDefinitionInjectsTimeoutSeconds(t *testing.T) {
	client := &Client{name: "test"}
	exec := NewExecutable(client, Tool{
		Name:        "echo",
		Description: "echo tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []any{},
		},
	})

	def := exec.Definition()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", def.Parameters["properties"])
	}
	timeoutProp, ok := props["timeout_seconds"].(map[string]any)
	if !ok {
		t.Fatalf("expected timeout_seconds property, got %v", props["timeout_seconds"])
	}
	if timeoutProp["type"] != "integer" {
		t.Fatalf("expected integer type, got %v", timeoutProp["type"])
	}
	// timeout_seconds is optional: it should not be in required.
	required, _ := def.Parameters["required"].([]any)
	for _, r := range required {
		if r == "timeout_seconds" {
			t.Fatal("timeout_seconds should not be required")
		}
	}
}

func TestExecutableDefinitionRespectsExistingTimeoutSeconds(t *testing.T) {
	client := &Client{name: "test"}
	exec := NewExecutable(client, Tool{
		Name:        "wait",
		Description: "wait tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "server timeout",
				},
			},
		},
	})

	def := exec.Definition()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", def.Parameters["properties"])
	}
	timeoutProp, ok := props["timeout_seconds"].(map[string]any)
	if !ok {
		t.Fatalf("expected timeout_seconds property, got %v", props["timeout_seconds"])
	}
	// We must not override the server's own description.
	if timeoutProp["description"] != "server timeout" {
		t.Fatalf("expected server description, got %v", timeoutProp["description"])
	}
}

func TestExecutableExecuteStripsTimeoutSeconds(t *testing.T) {
	ft := &fakeTransport{healthy: true, info: Info{Name: "fake"}, tools: []Tool{{Name: "echo"}}}
	client, err := NewClient("test", ft)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewExecutable(client, Tool{
		Name:        "echo",
		Description: "echo tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	})

	_, err = exec.Execute(context.Background(), "call_1", map[string]any{
		"message":         "hello",
		"timeout_seconds": float64(1),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if ft.lastCallParams == nil {
		t.Fatal("expected a tools/call to be recorded")
	}
	params, ok := ft.lastCallParams.(CallToolParams)
	if !ok {
		t.Fatalf("expected CallToolParams, got %T", ft.lastCallParams)
	}
	args := params.Arguments
	if _, exists := args["timeout_seconds"]; exists {
		t.Fatalf("timeout_seconds should be stripped from MCP args, got %v", args)
	}
	if args["message"] != "hello" {
		t.Fatalf("expected message arg preserved, got %v", args["message"])
	}
}

func TestExecutableExecuteAppliesTimeoutFromArg(t *testing.T) {
	ft := &fakeTransport{healthy: true, info: Info{Name: "fake"}, tools: []Tool{{Name: "echo"}}}
	client, err := NewClient("test", ft)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewExecutable(client, Tool{
		Name:        "echo",
		Description: "echo tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	})

	start := time.Now()
	_, _ = exec.Execute(context.Background(), "call_1", map[string]any{
		"timeout_seconds": float64(1),
	})
	if ft.lastCallCtx == nil {
		t.Fatal("expected call context to be recorded")
	}
	deadline, ok := ft.lastCallCtx.Deadline()
	if !ok {
		t.Fatal("expected a deadline on the call context")
	}
	remaining := time.Until(deadline)
	if remaining > 2*time.Second || remaining < 500*time.Millisecond {
		t.Fatalf("expected ~1s timeout, got %v (elapsed %v)", remaining, time.Since(start))
	}
}

func TestExecutableExecuteAppliesDefaultTimeout(t *testing.T) {
	ft := &fakeTransport{healthy: true, info: Info{Name: "fake"}, tools: []Tool{{Name: "echo"}}}
	client, err := NewClient("test", ft)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewExecutable(client, Tool{
		Name:        "echo",
		Description: "echo tool",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	})

	_, _ = exec.Execute(context.Background(), "call_1", map[string]any{})
	if ft.lastCallCtx == nil {
		t.Fatal("expected call context to be recorded")
	}
	deadline, ok := ft.lastCallCtx.Deadline()
	if !ok {
		t.Fatal("expected a deadline on the call context")
	}
	remaining := time.Until(deadline)
	if remaining < 110*time.Second || remaining > 130*time.Second {
		t.Fatalf("expected ~120s default timeout, got %v", remaining)
	}
}
