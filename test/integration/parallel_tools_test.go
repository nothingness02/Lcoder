//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agentsetup"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	_ "github.com/lcoder/lcoder/pkg/tools/builtin" // registers builtin tool factories via init()
)

// probeDelay is the fixed cost of one slow_probe call. It is long enough that
// concurrent calls produce clearly overlapping wall-clock intervals (real file
// reads finish in microseconds and cannot prove overlap), yet short enough that
// a handful of parallel probes stay well under the run timeout.
const probeDelay = 300 * time.Millisecond

// slowProbeTool is a deterministic, instrumented tool used only by this test. It
// sleeps for a fixed duration and returns a one-line result, so the agent's
// parallel execution path produces measurable overlapping intervals on the bus.
type slowProbeTool struct {
	delay time.Duration
}

func (t slowProbeTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "slow_probe",
		Description: "并行探测工具:接收一个 label 参数,内部固定耗时后返回。" +
			"当有多个独立 label 需要探测时,必须在同一轮(一条助手消息)中一次性并行调用本工具多次," +
			"每次传入一个不同的 label,禁止串行逐个等待。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{
					"type":        "string",
					"description": "本次探测的标签",
				},
			},
			"required": []any{"label"},
		},

	}
}

func (t slowProbeTool) Execute(ctx context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	label, _ := args["label"].(string)
	select {
	case <-time.After(t.delay):
	case <-ctx.Done():
		return models.ToolExecutionResult{}, ctx.Err()
	}
	return models.NewToolExecutionResultText(fmt.Sprintf("probe %q done", label)), nil
}

// probeInterval is one observed tool execution, with wall-clock start/end
// captured inside the (mutex-protected) bus handler. Relative-millisecond
// fields are filled in after the run, anchored at the earliest start.
type probeInterval struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Turn       int    `json:"turn"`
	StartMs    int64  `json:"start_ms"`
	EndMs      int64  `json:"end_ms"`
	DurMs      int64  `json:"dur_ms"`

	startWall time.Time
	endWall   time.Time
}

// probeReport is the structured output written to disk. It omits any credentials.
type probeReport struct {
	GeneratedAt    string          `json:"generated_at"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Prompt         string          `json:"prompt"`
	ProbeDelayMs   int64           `json:"probe_delay_ms"`
	TotalCalls     int             `json:"total_calls"`
	MaxConcurrency int             `json:"max_concurrency"`
	Intervals      []probeInterval `json:"intervals"`
	AssistantText  []string        `json:"assistant_text"`
	Error          string          `json:"error,omitempty"`
}

// maxConcurrency computes the peak number of simultaneously-running intervals via
// a sweep line over start (+1) and end (-1) events. Overlapping intervals are
// direct proof that the tools ran concurrently rather than sequentially.
func maxConcurrency(intervals []probeInterval) int {
	type ev struct {
		t     time.Time
		delta int
	}
	evs := make([]ev, 0, len(intervals)*2)
	for _, iv := range intervals {
		evs = append(evs, ev{t: iv.startWall, delta: 1})
		evs = append(evs, ev{t: iv.endWall, delta: -1})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].t.Equal(evs[j].t) {
			// Process ends before starts at the same instant so touching (non-
			// overlapping) intervals are not miscounted as concurrent.
			return evs[i].delta < evs[j].delta
		}
		return evs[i].t.Before(evs[j].t)
	})
	cur, peak := 0, 0
	for _, e := range evs {
		cur += e.delta
		if cur > peak {
			peak = cur
		}
	}
	return peak
}

// renderParallelTimeline draws an ASCII Gantt chart of the observed tool
// intervals (one row per call, positioned by relative milliseconds) plus a
// concurrency summary and the assistant's natural-language turns.
func renderParallelTimeline(report probeReport) string {
	var b strings.Builder
	b.WriteString("# Lcoder 集成测试 —— 并行工具调用时间线\n\n")
	b.WriteString(fmt.Sprintf("- Generated: %s\n", report.GeneratedAt))
	b.WriteString(fmt.Sprintf("- Provider / Model: %s / %s\n", report.Provider, report.Model))
	b.WriteString(fmt.Sprintf("- 单次 slow_probe 耗时: %d ms\n", report.ProbeDelayMs))
	b.WriteString(fmt.Sprintf("- 工具调用总数: %d\n", report.TotalCalls))
	b.WriteString(fmt.Sprintf("- 观测到的最大并发数: %d\n", report.MaxConcurrency))
	if report.Error != "" {
		b.WriteString(fmt.Sprintf("- Run error: %s\n", report.Error))
	}
	b.WriteString("\n时间区间由事件总线上的 `ToolExecutionStart/End` 事件在 handler 内用 `time.Now()` 抓取," +
		"区间重叠即真实并发的证据。\n\n---\n\n")

	// Section 1: ASCII timeline.
	b.WriteString("## 一、工具执行时间线(ASCII 甘特图)\n\n")
	if len(report.Intervals) == 0 {
		b.WriteString("_(无工具调用)_\n\n")
	} else {
		const cols = 60
		var maxEnd int64
		for _, iv := range report.Intervals {
			if iv.EndMs > maxEnd {
				maxEnd = iv.EndMs
			}
		}
		if maxEnd <= 0 {
			maxEnd = 1
		}
		b.WriteString("```text\n")
		b.WriteString(fmt.Sprintf("0ms %s %dms\n", strings.Repeat(" ", cols-2), maxEnd))
		for _, iv := range report.Intervals {
			startCol := int(int64(cols) * iv.StartMs / maxEnd)
			endCol := int(int64(cols) * iv.EndMs / maxEnd)
			if endCol <= startCol {
				endCol = startCol + 1
			}
			if endCol > cols {
				endCol = cols
			}
			bar := strings.Repeat(" ", startCol) + "[" + strings.Repeat("=", max0(endCol-startCol-1)) + "]"
			b.WriteString(fmt.Sprintf("%-44s |%s\n", fmt.Sprintf("turn%d %s", iv.Turn, iv.Name), bar))
		}
		b.WriteString("```\n\n")
	}

	// Section 2: interval table.
	b.WriteString("## 二、区间明细\n\n")
	b.WriteString("| # | turn | tool | start(ms) | end(ms) | dur(ms) |\n")
	b.WriteString("|---|------|------|-----------|---------|---------|\n")
	for i, iv := range report.Intervals {
		b.WriteString(fmt.Sprintf("| %d | %d | %s | %d | %d | %d |\n",
			i+1, iv.Turn, iv.Name, iv.StartMs, iv.EndMs, iv.DurMs))
	}
	b.WriteString("\n")

	// Section 3: concurrency verdict.
	b.WriteString("## 三、并发结论\n\n")
	verdict := "未观测到并发(maxConcurrency < 2)"
	if report.MaxConcurrency >= 2 {
		verdict = fmt.Sprintf("PASS —— 观测到 %d 个工具调用区间重叠,确认 agent 并发执行了同一轮的多个 tool_call", report.MaxConcurrency)
	}
	b.WriteString("- " + verdict + "\n\n---\n\n")

	// Section 4: assistant turns.
	b.WriteString("## 四、助手消息\n\n")
	if len(report.AssistantText) == 0 {
		b.WriteString("_(无)_\n")
	}
	for i, txt := range report.AssistantText {
		b.WriteString(fmt.Sprintf("### turn[%d]\n\n```text\n%s\n```\n\n", i, txt))
	}
	return b.String()
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func TestParallelToolCalls(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	client := llm.NewClient(buildEngineForTest(cfg))

	provider := selectProvider(cfg)
	if provider == "" {
		t.Skip("no provider with an API key configured; skipping parallel-tool integration test")
	}
	model := selectModel(cfg, client, provider)
	if model == "" {
		t.Skipf("no model resolvable for provider %q; set LCODER_IT_MODEL to override", provider)
	}

	ctx := context.Background()
	window, _ := client.ModelWindow(ctx, provider, model)
	maxOutput, _ := client.ModelMaxOutput(ctx, provider, model)
	cfgBudget, _ := cfg.ResolveContextBudget(window, maxOutput)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))

	// Minimal context (no project docs / skills) keeps the prompt small and the
	// run focused on tool-call behavior.
	mgr := agentsetup.NewContextManager(cfg, cfgBudget, "", client, "", "", nil, nil)

	// Registry rooted at the repo; register the instrumented slow_probe alongside
	// the builtins so the model has an explicit parallel target.
	registry := tools.NewRegistry(repoRoot)
	if err := registry.RegisterBuiltinFactories(repoRoot); err != nil {
		t.Fatalf("register builtin tools: %v", err)
	}
	registry.Register("slow_probe", slowProbeTool{delay: probeDelay})

	// Mutex-protected capture: parallel tools emit Start/End from concurrent
	// goroutines, so the handler must lock and stamp wall-clock time itself
	// (the events carry no timestamp).
	var (
		mu            sync.Mutex
		startWall     = map[string]time.Time{}
		intervals     []probeInterval
		assistantText []string
	)
	bus := events.New()
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch e := ev.(type) {
		case events.ToolExecutionStartEvent:
			mu.Lock()
			startWall[e.ToolCallID] = time.Now()
			mu.Unlock()
		case events.ToolExecutionEndEvent:
			now := time.Now()
			mu.Lock()
			intervals = append(intervals, probeInterval{
				ToolCallID: e.ToolCallID,
				Name:       e.ToolName,
				Turn:       e.Turn,
				startWall:  startWall[e.ToolCallID],
				endWall:    now,
			})
			delete(startWall, e.ToolCallID)
			mu.Unlock()
		case events.TurnEndEvent:
			if txt := strings.TrimSpace(e.Message.Text()); txt != "" {
				mu.Lock()
				assistantText = append(assistantText, txt)
				mu.Unlock()
			}
		}
		return nil
	})

	modeManager := agent.NewModeManager()
	_ = modeManager.LoadModes(agent.DefaultModeDirs(repoRoot))
	ag, err := agent.NewBuilder().
		WithConfig(agent.Config{
			SystemPrompt:      "",
			Model:             models.ModelRef{Provider: provider, ID: model},
			ContextManager:    mgr,
			Mode:              "code",
			ModeManager:       modeManager,
		}).
		WithGatewayClient(client).
		WithRegistry(registry).
		WithPermissions(permissions.NewEngineFromRules(nil)). // non-interactive: allow all
		WithEventBus(bus).
		WithObservability(observability.NewCollector(observability.NewMemoryExporter())).
		Build()
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}

	const prompt = "我需要并行探测三个独立目标,标签分别是 alpha、beta、gamma。" +
		"请在同一轮(一条助手消息)里一次性并行调用 slow_probe 工具三次,每次分别传入一个标签," +
		"不要串行逐个等待,也不要调用其它工具。三次探测都返回后,用一句话汇报三个探测均已完成。"

	report := probeReport{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Provider:     provider,
		Model:        model,
		Prompt:       prompt,
		ProbeDelayMs: probeDelay.Milliseconds(),
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := ag.Prompt(runCtx, models.UserMessage(prompt)); err != nil {
		report.Error = err.Error()
	}

	// Anchor relative milliseconds at the earliest observed start.
	mu.Lock()
	var t0 time.Time
	for _, iv := range intervals {
		if t0.IsZero() || iv.startWall.Before(t0) {
			t0 = iv.startWall
		}
	}
	for i := range intervals {
		intervals[i].StartMs = intervals[i].startWall.Sub(t0).Milliseconds()
		intervals[i].EndMs = intervals[i].endWall.Sub(t0).Milliseconds()
		intervals[i].DurMs = intervals[i].endWall.Sub(intervals[i].startWall).Milliseconds()
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].StartMs < intervals[j].StartMs })
	peak := maxConcurrency(intervals)
	mu.Unlock()

	report.Intervals = intervals
	report.TotalCalls = len(intervals)
	report.MaxConcurrency = peak
	report.AssistantText = assistantText

	// Write JSON + markdown visualization (always, even on assertion failure).
	outDir := filepath.Join(wd, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	ts := time.Now().Format("20060102_150405")
	jsonPath := filepath.Join(outDir, "parallel_tools_"+ts+".json")
	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = os.WriteFile(jsonPath, data, 0o644)
	}
	mdPath := filepath.Join(outDir, "parallel_tools_"+ts+".md")
	_ = os.WriteFile(mdPath, []byte(renderParallelTimeline(report)), 0o644)

	t.Logf("parallel-tool run complete: %d calls, max concurrency %d", report.TotalCalls, report.MaxConcurrency)
	t.Logf("report: %s", jsonPath)
	t.Logf("timeline markdown: %s", mdPath)

	if report.Error != "" {
		t.Fatalf("agent run returned error (report still written): %s", report.Error)
	}
	if report.TotalCalls < 2 {
		t.Fatalf("expected the model to issue >= 2 slow_probe calls; got %d (model may not have parallelized)", report.TotalCalls)
	}
	if report.MaxConcurrency < 2 {
		t.Fatalf("expected overlapping tool intervals (max concurrency >= 2); got %d — tools ran sequentially", report.MaxConcurrency)
	}
}
