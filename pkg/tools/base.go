package tools

import (
	"context"

	"github.com/lcoder/lcoder/pkg/models"
)

// Executable is the interface implemented by every tool available to the agent.
type Executable interface {
	Definition() models.ToolDefinition
	Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error)
}

// Factory creates a tool instance bound to a working directory.
type Factory func(cwd string) Executable
