package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// parallelTool declares OpNone (touches no parent resource) and blocks until
// release is closed, so tests can observe how many calls overlap.
type parallelTool struct {
	def      models.ToolDefinition
	release  chan struct{}
	inFlight int64
	peak     int64
}

func (t *parallelTool) Definition() models.ToolDefinition { return t.def }
func (t *parallelTool) DeclareAccesses(map[string]any) []tools.ToolAccess {
	return []tools.ToolAccess{{Op: tools.OpNone}}
}
func (t *parallelTool) Execute(_ context.Context, _ string, _ map[string]any) (models.ToolExecutionResult, error) {
	cur := atomic.AddInt64(&t.inFlight, 1)
	defer atomic.AddInt64(&t.inFlight, -1)
	for {
		p := atomic.LoadInt64(&t.peak)
		if cur <= p || atomic.CompareAndSwapInt64(&t.peak, p, cur) {
			break
		}
	}
	<-t.release
	return models.ToolExecutionResult{Content: []models.ContentPart{models.TextContent{Text: "done"}}}, nil
}

var _ tools.AccessDeclarer = (*parallelTool)(nil)

// Two independent subagent-style calls (OpNone) in one batch run in parallel:
// the second overlaps the first instead of waiting for it (the fix for the
// heterogeneous-task parallel gap; aligned with kimi's Promise.allSettled).
func TestBatchParallelOpNone(t *testing.T) {
	sub := &parallelTool{def: models.ToolDefinition{
		Name:        "subagent",
		Description: "delegate",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}}
	release := make(chan struct{})
	sub.release = release

	r := tools.NewRegistry(".")
	r.Register("subagent", sub)

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
		{ID: "sub1", Name: "subagent", Arguments: map[string]any{}},
		{ID: "sub2", Name: "subagent", Arguments: map[string]any{}},
	}

	done := make(chan struct{})
	go func() {
		_, _ = e.execute(context.Background(), 0, models.AgentMessage{}, calls)
		close(done)
	}()

	// Both calls must be in flight while still blocked: the peak overlaps.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&sub.peak) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("two OpNone calls did not run in parallel")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	<-done
}
