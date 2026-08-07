package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// A subagent_swarm call in a mixed batch is refused with corrective
// feedback, while the other calls in the batch execute normally
// (kimi-code's AgentSwarmExclusiveDeny, adapted to veto only the swarm call).
func TestSwarmExclusivityVeto(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register(builtin.SwarmToolName, fakeTool{fullDef(builtin.SwarmToolName, "Batch delegate.")})
	r.Register("read", fakeTool{fullDef("read", "Read file.")})

	cfg := Config{}
	e := &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: permissions.NewEngine(permissions.DefaultConfig()),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}

	calls := []models.ToolCallContent{
		{ID: "call_swarm", Name: builtin.SwarmToolName, Arguments: map[string]any{
			"prompt_template": "do {{item}}", "items": []any{"a", "b"},
		}},
		{ID: "call_read", Name: "read", Arguments: map[string]any{"path": "."}},
	}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	byID := make(map[string]models.ToolResultContent)
	for _, msg := range results {
		trc := msg.Content[0].(models.ToolResultContent)
		byID[trc.ToolCallID] = trc
	}

	swarm := byID["call_swarm"]
	if !swarm.IsError {
		t.Fatal("mixed swarm call must be refused")
	}
	if !strings.Contains(swarm.Text(), "ONLY tool call") {
		t.Fatalf("veto should carry the corrective message, got %q", swarm.Text())
	}
	if byID["call_read"].IsError {
		t.Fatalf("innocent calls in the batch must still execute, got %q", byID["call_read"].Text())
	}
}

// Multiple subagent_swarm calls in one batch each get the sequential
// guidance rather than the mixed-batch message.
func TestSwarmExclusivityMultipleSwarms(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register(builtin.SwarmToolName, fakeTool{fullDef(builtin.SwarmToolName, "Batch delegate.")})

	cfg := Config{}
	e := &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: permissions.NewEngine(permissions.DefaultConfig()),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}

	calls := []models.ToolCallContent{
		{ID: "s1", Name: builtin.SwarmToolName, Arguments: map[string]any{"prompt_template": "do {{item}}", "items": []any{"a", "b"}}},
		{ID: "s2", Name: builtin.SwarmToolName, Arguments: map[string]any{"prompt_template": "review {{item}}", "items": []any{"x", "y"}}},
	}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)

	byID := make(map[string]models.ToolResultContent)
	for _, msg := range results {
		trc := msg.Content[0].(models.ToolResultContent)
		byID[trc.ToolCallID] = trc
	}
	for _, id := range []string{"s1", "s2"} {
		trc := byID[id]
		if !trc.IsError {
			t.Fatalf("multiple swarm calls must each be vetoed, %s not", id)
		}
		if !strings.Contains(trc.Text(), "sequentially") {
			t.Fatalf("multiple-swarm veto should give sequential guidance, got %q", trc.Text())
		}
	}
}

// The same swarm call alone in its batch is NOT vetoed.
func TestSwarmExclusivityAllowsSoloBatch(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register(builtin.SwarmToolName, fakeTool{fullDef(builtin.SwarmToolName, "Batch delegate.")})

	cfg := Config{}
	e := &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    r,
		permissions: permissions.NewEngine(permissions.DefaultConfig()),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}

	calls := []models.ToolCallContent{
		{ID: "call_swarm", Name: builtin.SwarmToolName, Arguments: map[string]any{
			"prompt_template": "do {{item}}", "items": []any{"a", "b"},
		}},
	}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)

	trc := results[0].Content[0].(models.ToolResultContent)
	if trc.IsError && strings.Contains(trc.Text(), "ONLY tool call") {
		t.Fatalf("a solo swarm call must not be vetoed, got %q", trc.Text())
	}
}
