package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/subagent"
)

type fakeRunner struct {
	singleFn   func(ctx context.Context, agentName, task, cwd string) (string, error)
	parallelFn func(ctx context.Context, items []subagent.TaskItem) ([]subagent.Result, error)
	chainFn    func(ctx context.Context, items []subagent.ChainItem) (string, error)
}

func (f *fakeRunner) RunSingle(ctx context.Context, agentName, task, cwd string) (string, error) {
	if f.singleFn != nil {
		return f.singleFn(ctx, agentName, task, cwd)
	}
	return "", errors.New("RunSingle not implemented")
}

func (f *fakeRunner) RunParallel(ctx context.Context, items []subagent.TaskItem) ([]subagent.Result, error) {
	if f.parallelFn != nil {
		return f.parallelFn(ctx, items)
	}
	return nil, errors.New("RunParallel not implemented")
}

func (f *fakeRunner) RunChain(ctx context.Context, items []subagent.ChainItem) (string, error) {
	if f.chainFn != nil {
		return f.chainFn(ctx, items)
	}
	return "", errors.New("RunChain not implemented")
}

func TestDefinitionHasOneOfModes(t *testing.T) {
	tool := &subagentTool{runner: &fakeRunner{}}
	def := tool.Definition()
	if def.Name != "subagent" {
		t.Errorf("name = %q, want subagent", def.Name)
	}
	params, ok := def.Parameters["oneOf"].([]map[string]any)
	if !ok || len(params) != 3 {
		t.Fatalf("expected oneOf with 3 branches, got %v", def.Parameters["oneOf"])
	}
}

func TestParseItemsSuccess(t *testing.T) {
	input := []any{
		map[string]any{"agent": "a", "task": "t1", "cwd": "./foo"},
		map[string]any{"agent": "b", "task": "t2"},
	}
	got, err := parseItems(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []subagent.TaskItem{
		{Agent: "a", Task: "t1", CWD: "./foo"},
		{Agent: "b", Task: "t2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseItemsValidation(t *testing.T) {
	cases := []struct {
		name  string
		input []any
		want  string
	}{
		{
			name:  "not an object",
			input: []any{"bad"},
			want:  "item 0 is not an object",
		},
		{
			name:  "missing agent",
			input: []any{map[string]any{"task": "t"}},
			want:  "item 0 missing agent",
		},
		{
			name:  "missing task",
			input: []any{map[string]any{"agent": "a"}},
			want:  "item 0 missing task",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseItems(tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestExecuteSingle(t *testing.T) {
	runner := &fakeRunner{
		singleFn: func(_ context.Context, agentName, task, cwd string) (string, error) {
			if agentName != "worker" || task != "do it" || cwd != "./pkg" {
				t.Errorf("unexpected args: %s %s %s", agentName, task, cwd)
			}
			return "done", nil
		},
	}
	tool := &subagentTool{runner: runner}
	res, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"agent": "worker",
		"task":  "do it",
		"cwd":   "./pkg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text() != "done" {
		t.Errorf("text = %q, want done", res.Text())
	}
}

func TestExecuteSingleMissingTask(t *testing.T) {
	tool := &subagentTool{runner: &fakeRunner{}}
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"agent": "worker",
		"task":  "  ",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "task is required") {
		t.Errorf("error %q does not contain task is required", err.Error())
	}
}

func TestExecuteParallel(t *testing.T) {
	runner := &fakeRunner{
		parallelFn: func(_ context.Context, items []subagent.TaskItem) ([]subagent.Result, error) {
			want := []subagent.TaskItem{
				{Agent: "a", Task: "t1"},
				{Agent: "b", Task: "t2"},
			}
			if !reflect.DeepEqual(items, want) {
				t.Errorf("items = %+v, want %+v", items, want)
			}
			return []subagent.Result{{Text: "r1"}, {Text: "r2"}}, nil
		},
	}
	tool := &subagentTool{runner: runner}
	res, err := tool.Execute(context.Background(), "call-2", map[string]any{
		"tasks": []any{
			map[string]any{"agent": "a", "task": "t1"},
			map[string]any{"agent": "b", "task": "t2"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text() != "[0] r1\n\n[1] r2" {
		t.Errorf("text = %q", res.Text())
	}
}

func TestExecuteChain(t *testing.T) {
	runner := &fakeRunner{
		chainFn: func(_ context.Context, items []subagent.ChainItem) (string, error) {
			want := []subagent.ChainItem{
				{Agent: "scout", Task: "find"},
				{Agent: "worker", Task: "apply {previous}"},
			}
			if !reflect.DeepEqual(items, want) {
				t.Errorf("items = %+v, want %+v", items, want)
			}
			return "chained", nil
		},
	}
	tool := &subagentTool{runner: runner}
	res, err := tool.Execute(context.Background(), "call-3", map[string]any{
		"chain": []any{
			map[string]any{"agent": "scout", "task": "find"},
			map[string]any{"agent": "worker", "task": "apply {previous}"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text() != "chained" {
		t.Errorf("text = %q, want chained", res.Text())
	}
}

func TestExecuteInvalidMode(t *testing.T) {
	tool := &subagentTool{runner: &fakeRunner{}}
	cases := []map[string]any{
		{},
		{"agent": "a", "tasks": []any{map[string]any{"agent": "a", "task": "t"}}},
	}
	for i, args := range cases {
		_, err := tool.Execute(context.Background(), "call", args)
		if err == nil {
			t.Fatalf("case %d: expected error", i)
		}
		if !strings.Contains(err.Error(), "exactly one of agent, tasks, or chain") {
			t.Errorf("case %d: error %q", i, err.Error())
		}
	}
}

func TestFormatParallelResults(t *testing.T) {
	results := []subagent.Result{
		{Text: "ok"},
		{Err: errors.New("boom")},
	}
	got := formatParallelResults(results)
	want := "[0] ok\n\n[1] ERROR: boom"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
