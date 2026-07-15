package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCWD(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	weird := filepath.Join(root, "..foo")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Fatalf("create ..foo dir: %v", err)
	}
	outside := filepath.Join(root, "..", "etc")

	r := &DefaultRunner{projectRoot: root}

	cases := []struct {
		name    string
		cwd     string
		wantErr bool
		wantIn  string
	}{
		{
			name:    "empty cwd falls back to project root",
			cwd:     "",
			wantErr: false,
		},
		{
			name:    "valid subdirectory",
			cwd:     sub,
			wantErr: false,
		},
		{
			name:    "directory escapes root via parent",
			cwd:     outside,
			wantErr: true,
			wantIn:  "outside project root",
		},
		{
			name:    "directory named ..foo inside root is allowed",
			cwd:     weird,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.validateCWD(tc.cwd)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantIn)
				}
				if !strings.Contains(err.Error(), tc.wantIn) {
					t.Fatalf("expected error containing %q, got %q", tc.wantIn, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.cwd == "" && got != root {
				t.Fatalf("empty cwd should resolve to project root: got %q, want %q", got, root)
			}
		})
	}
}

// fakeRunner is a test implementation of Runner that records call order and
// returns results based on task contents.
type fakeRunner struct {
	calls      []string
	returnErrs map[string]error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{returnErrs: make(map[string]error)}
}

func (f *fakeRunner) RunSingle(ctx context.Context, agentName string, task string, cwd string) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s:%s", agentName, task))
	if err := f.returnErrs[task]; err != nil {
		return "", err
	}
	return "result-" + task, nil
}

func (f *fakeRunner) RunParallel(ctx context.Context, items []TaskItem) ([]Result, error) {
	results := make([]Result, len(items))
	for i, item := range items {
		text, err := f.RunSingle(ctx, item.Agent, item.Task, item.CWD)
		results[i] = Result{Text: text, Err: err}
	}
	return results, nil
}

func (f *fakeRunner) RunChain(ctx context.Context, items []ChainItem) (string, error) {
	previous := ""
	for _, item := range items {
		task := strings.ReplaceAll(item.Task, "{previous}", previous)
		text, err := f.RunSingle(ctx, item.Agent, task, item.CWD)
		if err != nil {
			return "", err
		}
		previous = text
	}
	return previous, nil
}

func TestRunParallelOrderingAndPerResultErrors(t *testing.T) {
	f := newFakeRunner()
	f.returnErrs["b"] = errors.New("task b failed")

	items := []TaskItem{
		{Agent: "ag1", Task: "a"},
		{Agent: "ag2", Task: "b"},
		{Agent: "ag3", Task: "c"},
	}
	results, err := f.RunParallel(context.Background(), items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Results must be in item order, not call order.
	wantTexts := []string{"result-a", "", "result-c"}
	wantErrs := []bool{false, true, false}
	for i, res := range results {
		if res.Text != wantTexts[i] {
			t.Errorf("result[%d].Text = %q, want %q", i, res.Text, wantTexts[i])
		}
		if wantErrs[i] && res.Err == nil {
			t.Errorf("result[%d].Err = nil, want error", i)
		}
		if !wantErrs[i] && res.Err != nil {
			t.Errorf("result[%d].Err = %v, want nil", i, res.Err)
		}
	}

	// Successful tasks should continue even when one fails.
	if len(f.calls) != len(items) {
		t.Errorf("got %d calls, want %d", len(f.calls), len(items))
	}
}

func TestRunChainPreviousSubstitution(t *testing.T) {
	f := newFakeRunner()
	items := []ChainItem{
		{Agent: "ag1", Task: "first"},
		{Agent: "ag2", Task: "second {previous} tail"},
		{Agent: "ag3", Task: "third {previous}"},
	}
	got, err := f.RunChain(context.Background(), items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "result-third result-second result-first tail"
	if got != want {
		t.Fatalf("RunChain = %q, want %q", got, want)
	}

	wantCalls := []string{
		"ag1:first",
		"ag2:second result-first tail",
		"ag3:third result-second result-first tail",
	}
	for i, call := range f.calls {
		if call != wantCalls[i] {
			t.Errorf("call[%d] = %q, want %q", i, call, wantCalls[i])
		}
	}
}

func TestDefaultRunnerValidateCWD(t *testing.T) {
	root := t.TempDir()
	r := &DefaultRunner{projectRoot: root}
	_, err := r.validateCWD(filepath.Join(root, "..", "etc"))
	if err == nil {
		t.Fatal("expected cwd outside project root error")
	}
}
