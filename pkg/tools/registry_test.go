package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

type errorExecutable struct{ business bool }

func (e errorExecutable) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "error", ExecutionMode: models.ExecutionParallel}
}

func (e errorExecutable) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	if e.business {
		return models.NewToolExecutionResultError("business failure"), ErrToolExecution
	}
	return models.ToolExecutionResult{}, errors.New("system failure")
}

func TestRegistry_Execute_SystemError(t *testing.T) {
	r := NewRegistry(".")
	r.Register("error", errorExecutable{business: false})
	res, isError := r.Execute(context.Background(), "call_1", "error", nil)
	if !isError {
		t.Fatal("expected system error")
	}
	if res.Text() == "" {
		t.Fatal("expected error text")
	}
}

func TestRegistry_Execute_BusinessErrorNotSystemError(t *testing.T) {
	r := NewRegistry(".")
	r.Register("error", errorExecutable{business: true})
	res, isError := r.Execute(context.Background(), "call_1", "error", nil)
	if isError {
		t.Fatal("business failure should not be a system error")
	}
	if res.Text() == "" {
		t.Fatal("expected error text")
	}
}

func TestRegistry_Execute_UnknownToolIsSystemError(t *testing.T) {
	r := NewRegistry(".")
	_, isError := r.Execute(context.Background(), "call_1", "missing", nil)
	if !isError {
		t.Fatal("unknown tool should be a system error")
	}
}
