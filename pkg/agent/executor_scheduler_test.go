package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// gateTool blocks in Execute until released. It deliberately does NOT
// implement tools.AccessDeclarer; declaringGateTool adds that.
type gateTool struct {
	mu      sync.Mutex
	started []string
	release chan struct{}
}

func newGateTool() *gateTool { return &gateTool{release: make(chan struct{})} }

func (g *gateTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "gate",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
		},
	}
}

func (g *gateTool) Execute(ctx context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	p, _ := args["path"].(string)
	g.mu.Lock()
	g.started = append(g.started, p)
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return models.NewToolExecutionResultText("done"), nil
}

func (g *gateTool) startedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.started)
}

type declaringGateTool struct {
	*gateTool
	name string
	op   tools.AccessOperation
}

func (d declaringGateTool) Definition() models.ToolDefinition {
	def := d.gateTool.Definition()
	def.Name = d.name
	return def
}

func (d declaringGateTool) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	p, _ := args["path"].(string)
	return []tools.ToolAccess{{Op: d.op, Path: p}}
}

func newSchedulerTestExecutor(reg *tools.Registry) *executor {
	cfg := Config{}
	return &executor{
		cfg:         &cfg,
		mgr:         contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}),
		registry:    reg,
		permissions: permissions.NewEngine(permissions.DefaultConfig()),
		emitter:     &eventEmitter{bus: events.New()},
		dedup:       make(map[string]models.AgentMessage),
	}
}

func schedCall(id, name, path string) models.ToolCallContent {
	return models.ToolCallContent{Type: "tool_call", ID: id, Name: name, Arguments: map[string]any{"path": path}}
}

func waitForStarts(t *testing.T, g *gateTool, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for g.startedCount() < n {
		select {
		case <-deadline:
			t.Fatalf("expected %d started calls, got %v", n, g.started)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestExecutorDifferentFilesOverlap(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "gate", "a.go"), schedCall("c2", "gate", "b.go")}
	done := make(chan struct{})
	go func() {
		e.execute(context.Background(), 0, models.AgentMessage{}, calls)
		close(done)
	}()

	waitForStarts(t, gate, 2) // 串行调度下第二个永远不会 start
	close(gate.release)
	<-done
}

func TestExecutorSameFileSerializes(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "gate", "x.go"), schedCall("c2", "gate", "x.go")}
	done := make(chan []models.AgentMessage, 1)
	go func() {
		results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)
		done <- results
	}()

	waitForStarts(t, gate, 1)
	select {
	case <-time.After(100 * time.Millisecond):
	case <-done:
		t.Fatal("batch finished while first call was blocked")
	}
	if n := gate.startedCount(); n != 1 {
		t.Fatalf("conflicting call started early, started = %v", gate.started)
	}
	close(gate.release)
	if results := <-done; len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// 未实现 AccessDeclarer 的工具默认 OpAll:即便"不同文件"也串行。
func TestExecutorUndeclaredToolDefaultsToAll(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("gate", gate)
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "gate", "a.go"), schedCall("c2", "gate", "b.go")}
	done := make(chan struct{})
	go func() {
		e.execute(context.Background(), 0, models.AgentMessage{}, calls)
		close(done)
	}()

	waitForStarts(t, gate, 1)
	select {
	case <-time.After(100 * time.Millisecond):
	case <-done:
		t.Fatal("undeclared tool calls must serialize")
	}
	if n := gate.startedCount(); n != 1 {
		t.Fatalf("undeclared tool ran concurrently, started = %v", gate.started)
	}
	close(gate.release)
	<-done
}

// 交互式权限确认在 prepare 阶段串行发生:两个确认都完成后才有任何执行,
// 且顺序即 provider 顺序。
func TestExecutorConfirmsInProviderOrder(t *testing.T) {
	gate := newGateTool()
	close(gate.release) // 不阻塞执行
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	var mu sync.Mutex
	var order []string
	e.cfg.UserConfirm = confirmFunc(func(_ context.Context, info ToolCallInfo) (ConfirmResult, error) {
		mu.Lock()
		order = append(order, info.ToolCall.ID)
		mu.Unlock()
		return ConfirmResult{Allow: true, Scope: ScopeOnce}, nil
	})
	e.permissions = permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "gate", Pattern: "*", Decision: permissions.Ask},
	})

	calls := []models.ToolCallContent{schedCall("c1", "gate", "a.go"), schedCall("c2", "gate", "b.go")}
	e.execute(context.Background(), 0, models.AgentMessage{}, calls)

	if len(order) != 2 || order[0] != "c1" || order[1] != "c2" {
		t.Fatalf("confirmations out of provider order: %v", order)
	}
}

// prepare 阶段被拒的调用不占调度位,不阻塞后续调用。
func TestExecutorDeniedCallDoesNotBlock(t *testing.T) {
	gate := newGateTool()
	close(gate.release)
	reg := tools.NewRegistry(".")
	reg.Register("gate", declaringGateTool{gateTool: gate, name: "gate", op: tools.OpReadWrite})
	e := newSchedulerTestExecutor(reg)

	// "../escape.go" 被路径守卫拒绝(prepare 阶段),c2 必须正常执行。
	calls := []models.ToolCallContent{schedCall("c1", "gate", "../escape.go"), schedCall("c2", "gate", "b.go")}
	results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	first := results[0].Content[0].(models.ToolResultContent)
	if !first.IsError {
		t.Fatal("path-guarded call must be an error result")
	}
	second := results[1].Content[0].(models.ToolResultContent)
	if second.IsError {
		t.Fatalf("later call must not be affected, got %q", second.Text())
	}
	if gate.startedCount() != 1 {
		t.Fatalf("denied call must never execute, started = %v", gate.started)
	}
}

// 同批次重复的可缓存调用通过 addWait 边等待原调用,dedup 必中:
// 工具只执行一次,第二份结果标记 deduplicated。
func TestExecutorDuplicateCacheableCallDeduplicates(t *testing.T) {
	gate := newGateTool()
	reg := tools.NewRegistry(".")
	reg.Register("read", declaringGateTool{gateTool: gate, name: "read", op: tools.OpRead})
	e := newSchedulerTestExecutor(reg)

	calls := []models.ToolCallContent{schedCall("c1", "read", "same.go"), schedCall("c2", "read", "same.go")}
	done := make(chan []models.AgentMessage, 1)
	go func() {
		results, _ := e.execute(context.Background(), 0, models.AgentMessage{}, calls)
		done <- results
	}()

	waitForStarts(t, gate, 1)
	close(gate.release)
	results := <-done

	if gate.startedCount() != 1 {
		t.Fatalf("duplicate read must not re-execute, started = %v", gate.started)
	}
	trc := results[1].Content[0].(models.ToolResultContent)
	if trc.Details["deduplicated"] != true {
		t.Fatalf("expected deduplicated detail on second result, got %v", trc.Details)
	}
}

type confirmFunc func(context.Context, ToolCallInfo) (ConfirmResult, error)

func (f confirmFunc) Confirm(ctx context.Context, info ToolCallInfo) (bool, error) {
	res, err := f(ctx, info)
	return res.Allow, err
}

func (f confirmFunc) ConfirmWithScope(ctx context.Context, info ToolCallInfo) (ConfirmResult, error) {
	return f(ctx, info)
}
