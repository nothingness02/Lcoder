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
)

// A parallel/chain subagent call in a mixed batch is refused with corrective
// feedback, while the other calls in the batch execute normally
// (kimi-code's AgentSwarmExclusiveDeny, adapted to veto only the swarm call).
func TestSwarmExclusivityVeto(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register("subagent", fakeTool{fullDef("subagent", "Delegate tasks.")})
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
		{ID: "call_swarm", Name: "subagent", Arguments: map[string]any{
			"agent": "coder", "prompt_template": "do {{item}}", "items": []any{"a", "b"},
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
		t.Fatal("mixed parallel subagent call must be refused")
	}
	if !strings.Contains(swarm.Text(), "ONLY tool call") {
		t.Fatalf("veto should carry the corrective message, got %q", swarm.Text())
	}
	if byID["call_read"].IsError {
		t.Fatalf("innocent calls in the batch must still execute, got %q", byID["call_read"].Text())
	}
}

// The same parallel call alone in its batch is NOT vetoed.
func TestSwarmExclusivityAllowsSoloBatch(t *testing.T) {
	r := tools.NewRegistry(".")
	r.Register("subagent", fakeTool{fullDef("subagent", "Delegate tasks.")})

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
		{ID: "call_swarm", Name: "subagent", Arguments: map[string]any{
			"agent": "coder", "prompt_template": "do {{item}}", "items": []any{"a", "b"},
		}},
	}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)

	trc := results[0].Content[0].(models.ToolResultContent)
	if trc.IsError && strings.Contains(trc.Text(), "ONLY tool call") {
		t.Fatalf("a solo parallel call must not be vetoed, got %q", trc.Text())
	}
}
