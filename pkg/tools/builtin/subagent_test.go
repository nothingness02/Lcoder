package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
)

type fakeSubagentRunner struct {
	singleFn   func(ctx context.Context, agentName, task, cwd string) (string, error)
	parallelFn func(ctx context.Context, items []subagent.TaskItem) ([]subagent.Result, error)
	chainFn    func(ctx context.Context, items []subagent.TaskItem) (string, error)
}

func (f *fakeSubagentRunner) RunSingle(ctx context.Context, agentName, task, cwd string) (string, error) {
	if f.singleFn != nil {
		return f.singleFn(ctx, agentName, task, cwd)
	}
	return "", errors.New("RunSingle not implemented")
}

func (f *fakeSubagentRunner) RunParallel(ctx context.Context, items []subagent.TaskItem) ([]subagent.Result, error) {
	if f.parallelFn != nil {
		return f.parallelFn(ctx, items)
	}
	return nil, errors.New("RunParallel not implemented")
}

func (f *fakeSubagentRunner) RunChain(ctx context.Context, items []subagent.TaskItem) (string, error) {
	if f.chainFn != nil {
		return f.chainFn(ctx, items)
	}
	return "", errors.New("RunChain not implemented")
}

func TestSubagentDefinition(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSubagentRunner{})
	def := tool.Definition()
	if def.Name != "subagent" {
		t.Errorf("name = %q, want subagent", def.Name)
	}
	if def.ExecutionMode != models.ExecutionSequential {
		t.Errorf("execution mode = %q, want sequential", def.ExecutionMode)
	}
	oneOf, ok := def.Parameters["oneOf"].([]map[string]any)
	if !ok || len(oneOf) != 3 {
		t.Errorf("oneOf = %v, want 3 branches", def.Parameters["oneOf"])
	}
}

func TestSubagentSingle(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSubagentRunner{
		singleFn: func(_ context.Context, agentName, task, cwd string) (string, error) {
			return "single:" + agentName + ":" + task + ":" + cwd, nil
		},
	})
	res, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"agent": "worker",
		"task":  "do it",
		"cwd":   "./pkg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.Text(); got != "single:worker:do it:./pkg" {
		t.Errorf("text = %q", got)
	}
}

func TestSubagentParallel(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSubagentRunner{
		parallelFn: func(_ context.Context, items []subagent.TaskItem) ([]subagent.Result, error) {
			results := make([]subagent.Result, len(items))
			for i, it := range items {
				if it.Agent == "fail" {
					results[i] = subagent.Result{Err: errors.New("boom")}
					continue
				}
				results[i] = subagent.Result{Text: it.Task + " by " + it.Agent}
			}
			return results, nil
		},
	})
	res, err := tool.Execute(context.Background(), "call-2", map[string]any{
		"tasks": []any{
			map[string]any{"agent": "a", "task": "t1"},
			map[string]any{"agent": "fail", "task": "t2"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := res.Text()
	if !strings.Contains(text, "[0] t1 by a") {
		t.Errorf("missing success result: %q", text)
	}
	if !strings.Contains(text, "[1] ERROR: boom") {
		t.Errorf("missing error result: %q", text)
	}
}

func TestSubagentChain(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSubagentRunner{
		chainFn: func(_ context.Context, items []subagent.TaskItem) (string, error) {
			return "chain:" + items[0].Task, nil
		},
	})
	res, err := tool.Execute(context.Background(), "call-3", map[string]any{
		"chain": []any{
			map[string]any{"agent": "scout", "task": "find files"},
			map[string]any{"agent": "worker", "task": "edit based on {previous}"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.Text(); got != "chain:find files" {
		t.Errorf("text = %q", got)
	}
}

func TestSubagentValidation(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSubagentRunner{})
	cases := []struct {
		name string
		args map[string]any
	}{
		{
			name: "no mode",
			args: map[string]any{},
		},
		{
			name: "multiple modes",
			args: map[string]any{
				"agent": "a",
				"task":  "t",
				"tasks": []any{map[string]any{"agent": "a", "task": "t"}},
			},
		},
		{
			name: "empty single task",
			args: map[string]any{"agent": "a", "task": "   "},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "call", tc.args)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSubagentPropagatesRunnerError(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSubagentRunner{
		singleFn: func(context.Context, string, string, string) (string, error) {
			return "", errors.New("runner failed")
		},
	})
	_, err := tool.Execute(context.Background(), "call", map[string]any{
		"agent": "a",
		"task":  "t",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "runner failed") {
		t.Errorf("error = %v", err)
	}
}
