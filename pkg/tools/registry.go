package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
)

// Registry holds all available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Executable
	cwd   string
}

// NewRegistry creates an empty registry bound to a working directory.
func NewRegistry(cwd string) *Registry {
	return &Registry{
		tools: make(map[string]Executable),
		cwd:   cwd,
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(name string, exec Executable) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = exec
}

// RegisterBuiltin adds a built-in tool factory.
func (r *Registry) RegisterBuiltin(factory Factory) {
	exec := factory(r.cwd)
	r.Register(exec.Definition().Name, exec)
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Executable, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	exec, ok := r.tools[name]
	return exec, ok
}

// Definitions returns tool definitions for the LLM, sorted by name so the
// tool list — and with it the prompt cache prefix — stays stable across turns.
func (r *Registry) Definitions() []models.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]models.ToolDefinition, 0, len(r.tools))
	for _, exec := range r.tools {
		defs = append(defs, exec.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// Has reports whether a tool is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// Execute runs a tool by name. The second return value reports whether the
// result is an error the model should see: unknown tool names, tool-returned
// Go errors, panics, and results the tool itself flagged via IsError all
// count. Business outcomes the model can act on (e.g. a non-zero shell exit)
// should be returned as (result, nil) with IsError set on the result.
func (r *Registry) Execute(ctx context.Context, callID string, name string, args map[string]any) (result models.ToolExecutionResult, isError bool) {
	exec, ok := r.Get(name)
	if !ok {
		return models.NewToolExecutionResultError(fmt.Sprintf("Unknown tool: %s", name)), true
	}
	// A panicking tool must not crash the agent loop; degrade it to an error
	// tool_result so the model can try a different approach.
	defer func() {
		if rec := recover(); rec != nil {
			result = models.NewToolExecutionResultError(fmt.Sprintf("tool %q panicked: %v", name, rec))
			isError = true
		}
	}()
	res, err := exec.Execute(ctx, callID, args)
	if err != nil {
		return models.NewToolExecutionResultError(err.Error()), true
	}
	if res.IsError {
		return res, true
	}
	return res, false
}

// Without returns a shallow copy of the registry excluding the named tools.
// Used to strip delegation tools from a subagent that may not nest further.
func (r *Registry) Without(names ...string) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	excluded := make(map[string]bool, len(names))
	for _, n := range names {
		excluded[n] = true
	}
	out := NewRegistry(r.cwd)
	for name, exec := range r.tools {
		if !excluded[name] {
			out.tools[name] = exec
		}
	}
	return out
}
