package agent

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// fakeTool is a minimal Executable for agent-level deferred-loading tests.
type fakeTool struct {
	def models.ToolDefinition
}

func (f fakeTool) Definition() models.ToolDefinition { return f.def }
func (f fakeTool) Execute(_ context.Context, _ string, _ map[string]any) (models.ToolExecutionResult, error) {
	return models.ToolExecutionResult{}, nil
}

func fullDef(name, desc string) models.ToolDefinition {
	return models.ToolDefinition{
		Name:        name,
		Description: desc,
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []any{"path"},
		},
	}
}

func TestBaseToolDefinitions_Deferred(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register("read", fakeTool{fullDef("read", "Read file.")})
	r.Register("bash", fakeTool{fullDef("bash", "Run shell.")})
	r.Register("edit", fakeTool{fullDef("edit", "Edit file.")})
	r.Register("grep", fakeTool{fullDef("grep", "Search.")})

	ag := &Agent{
		cfg:      Config{DeferredTools: true, CoreTools: []string{"read"}},
		registry: r,
		bus:      events.New(),
	}
	ag.executor = &executor{cfg: &ag.cfg, registry: r, emitter: &eventEmitter{bus: ag.bus}}

	defs := ag.executor.baseToolDefinitions()
	var hasSearch, hasReadFull, hasEditStub bool
	for _, d := range defs {
		switch d.Name {
		case tools.ToolSearchName:
			hasSearch = true
		case "read":
			hasReadFull = d.Parameters != nil
		case "edit":
			hasEditStub = d.Parameters == nil
		}
	}
	if !hasSearch {
		t.Fatalf("expected tool_search in active set")
	}
	if !hasReadFull {
		t.Fatalf("expected read to keep full schema")
	}
	if !hasEditStub {
		t.Fatalf("expected edit to be a deferred stub")
	}
}

func TestDeferredToolPromotedAfterSearch(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register("read", fakeTool{fullDef("read", "Read file.")})
	r.Register("edit", fakeTool{fullDef("edit", "Edit file.")})

	ag := &Agent{
		cfg:      Config{DeferredTools: true, CoreTools: []string{"read"}},
		registry: r,
		bus:      events.New(),
	}
	ag.executor = &executor{cfg: &ag.cfg, registry: r, emitter: &eventEmitter{bus: ag.bus}}

	// Before tool_search, edit is a stub.
	before := ag.executor.baseToolDefinitions()
	var editBefore *models.ToolDefinition
	for i, d := range before {
		if d.Name == "edit" {
			editBefore = &before[i]
			break
		}
	}
	if editBefore == nil {
		t.Fatal("expected edit in deferred set before search")
	}
	if editBefore.Parameters != nil {
		t.Fatal("expected edit to be a stub before tool_search")
	}

	// Simulate the model calling tool_search("edit").
	ag.executor.handleToolSearch(context.Background(), 0, models.AgentMessage{}, models.ToolCallContent{
		ID:        "call_1",
		Name:      tools.ToolSearchName,
		Arguments: map[string]any{"query": "edit"},
	})

	// After activation, edit must carry the full schema.
	after := ag.executor.baseToolDefinitions()
	var editAfter *models.ToolDefinition
	for i, d := range after {
		if d.Name == "edit" {
			editAfter = &after[i]
			break
		}
	}
	if editAfter == nil {
		t.Fatal("expected edit in tool set after search")
	}
	if editAfter.Parameters == nil {
		t.Fatal("expected edit to be promoted to full schema after tool_search")
	}
}

func TestBaseToolDefinitions_NonDeferred(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register("read", fakeTool{fullDef("read", "Read file.")})

	ag := &Agent{cfg: Config{}, registry: r}
	ag.executor = &executor{cfg: &ag.cfg, registry: r, emitter: &eventEmitter{bus: ag.bus}}
	defs := ag.executor.baseToolDefinitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs without deferral (read + switch_mode), got %d", len(defs))
	}
	found := false
	for _, d := range defs {
		if d.Name == switchModeToolName {
			found = true
		}
		if d.Parameters == nil {
			t.Fatalf("expected full schema without deferral")
		}
	}
	if !found {
		t.Fatal("expected switch_mode definition to be included")
	}
}
