package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

type errorExecutable struct{ panics bool }

func (e errorExecutable) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "error"}
}

func (e errorExecutable) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	if e.panics {
		panic("boom")
	}
	return models.ToolExecutionResult{}, errors.New("system failure")
}

// businessExecutable reports a failure via the result's IsError flag while
// returning a nil Go error (e.g. a non-zero shell exit).
type businessExecutable struct{}

func (businessExecutable) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "business"}
}

func (businessExecutable) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	return models.NewToolExecutionResultError("business failure"), nil
}

func TestRegistry_Execute_ToolError(t *testing.T) {
	r := NewRegistry(".")
	r.Register("error", errorExecutable{})
	res, isError := r.Execute(context.Background(), "call_1", "error", nil)
	if !isError {
		t.Fatal("expected error result")
	}
	if res.Text() == "" {
		t.Fatal("expected error text")
	}
}

func TestRegistry_Execute_ResultFlaggedIsError(t *testing.T) {
	r := NewRegistry(".")
	r.Register("business", businessExecutable{})
	res, isError := r.Execute(context.Background(), "call_1", "business", nil)
	if !isError {
		t.Fatal("result flagged IsError should surface as an error")
	}
	if res.Text() != "business failure" {
		t.Fatalf("expected tool content preserved, got %q", res.Text())
	}
}

func TestRegistry_Execute_UnknownToolIsError(t *testing.T) {
	r := NewRegistry(".")
	_, isError := r.Execute(context.Background(), "call_1", "missing", nil)
	if !isError {
		t.Fatal("unknown tool should be an error result")
	}
}

func TestRegistry_Execute_PanicBecomesErrorResult(t *testing.T) {
	r := NewRegistry(".")
	r.Register("error", errorExecutable{panics: true})
	res, isError := r.Execute(context.Background(), "call_1", "error", nil)
	if !isError {
		t.Fatal("panicking tool should be an error result")
	}
	if !strings.Contains(res.Text(), "panicked") {
		t.Fatalf("expected panic notice, got %q", res.Text())
	}
}

type namedExecutable struct{ name string }

func (n namedExecutable) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: n.name}
}

func (n namedExecutable) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	return models.NewToolExecutionResultText("ok"), nil
}

func TestRegistry_DefinitionsSortedByName(t *testing.T) {
	r := NewRegistry(".")
	// Register in non-alphabetical order; Definitions must sort so the tool
	// list (and prompt cache prefix) is stable.
	for _, name := range []string{"write", "read", "bash", "edit"} {
		r.Register(name, namedExecutable{name: name})
	}
	defs := r.Definitions()
	var got []string
	for _, d := range defs {
		got = append(got, d.Name)
	}
	want := []string{"bash", "edit", "read", "write"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("definitions order = %v, want %v", got, want)
	}
}

// cwdProbe is a builtin-style factory that records the cwd it was built with.
func TestRegistry_WithCWD_RebuildsBuiltins(t *testing.T) {
	var builtWith []string
	DefaultFactories.Register("cwd-probe", func(cwd string) Executable {
		builtWith = append(builtWith, cwd)
		return namedExecutable{name: "cwd-probe"}
	})
	defer func() {
		DefaultFactories.Unregister("cwd-probe")
	}()

	r := NewRegistry("/a")
	r.Register("manual", namedExecutable{name: "manual"})
	// Register an instance the factory can rebuild: WithCWD re-instantiates
	// tools whose factory is in DefaultFactories.
	r.Register("cwd-probe", namedExecutable{name: "cwd-probe"})

	got := r.WithCWD("/b")
	if got.cwd != "/b" {
		t.Fatalf("WithCWD cwd = %q, want /b", got.cwd)
	}
	if len(builtWith) != 1 || builtWith[0] != "/b" {
		t.Fatalf("builtin rebuilt with %v, want [/b]", builtWith)
	}
	// The original registry is untouched and manual tools survive by instance.
	if r.cwd != "/a" {
		t.Fatalf("original registry cwd changed to %q", r.cwd)
	}
	if _, ok := got.Get("manual"); !ok {
		t.Fatal("manual tool must survive WithCWD")
	}
}
