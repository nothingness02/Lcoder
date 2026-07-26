//go:build integration

// This file measures how wiring the code index into the agent loop changes
// agent behavior on a real codebase. It runs the SAME question against two
// freshly built agents pointed at reference/Kocoro (a ~700-file Go project):
//
//   - baseline:  no code index; the agent explores with ls/grep/read only.
//   - codeindex: code index enabled; the repo_index tool is registered exactly
//     as cmd/lcoder/main.go wires it (sqlitestore indexer + Injector).
//
// For each run it captures the tool-call chain from the event bus and the
// token consumption from the observability collector's metrics, then writes a
// JSON report and a side-by-side markdown comparison to test/integration/output/.
//
// Run with (requires real provider credentials, e.g. DEEPSEEK_API_KEY):
//
//	go test -tags integration ./test/integration/ -run TestCodeIndexCompare -v -timeout 15m
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agentsetup"
	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/codeindex/sqlitestore"
	"github.com/lcoder/lcoder/pkg/config"
	contextloader "github.com/lcoder/lcoder/pkg/context"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
	builtinTools "github.com/lcoder/lcoder/pkg/tools/builtin"
)

// toolLink is one step in the observed tool-call chain.
type toolLink struct {
	Turn    int    `json:"turn"`
	Name    string `json:"name"`
	KeyArg  string `json:"key_arg"`
	IsError bool   `json:"is_error"`
}

// indexMetrics summarizes the code-index build for the codeindex run.
type indexMetrics struct {
	DBPath         string `json:"db_path"`
	IndexLatencyMS int64  `json:"index_latency_ms"`
	Files          int64  `json:"files"`
	Nodes          int64  `json:"nodes"`
	Edges          int64  `json:"edges"`
	Unresolved     int64  `json:"unresolved"`
}

// compareRunReport holds everything measured for one agent run.
type compareRunReport struct {
	Name             string        `json:"name"`
	CodeIndex        bool          `json:"code_index"`
	WallTimeMS       int64         `json:"wall_time_ms"`
	Turns            int           `json:"turns"`
	ToolChain        []toolLink    `json:"tool_chain"`
	PromptTokens     int64         `json:"prompt_tokens"`
	CompletionTokens int64         `json:"completion_tokens"`
	TotalTokens      int64         `json:"total_tokens"`
	CacheReadTokens  int64         `json:"cache_read_tokens"`
	CostUSD          float64       `json:"cost_usd"`
	Index            *indexMetrics `json:"index,omitempty"`
	FinalAnswer      string        `json:"final_answer"`
	Error            string        `json:"error,omitempty"`
}

// compareReport is the top-level artifact written to disk.
type compareReport struct {
	GeneratedAt string             `json:"generated_at"`
	Provider    string             `json:"provider"`
	Model       string             `json:"model"`
	Target      string             `json:"target"`
	Prompt      string             `json:"prompt"`
	Runs        []compareRunReport `json:"runs"`
}

// keyArgOf extracts the most telling argument of a tool call for chain display.
func keyArgOf(args map[string]any) string {
	for _, k := range []string{"query", "command", "path", "pattern"} {
		if v, ok := args[k]; ok {
			s := fmt.Sprintf("%v", v)
			s = strings.ReplaceAll(s, "\n", " ")
			if len(s) > 80 {
				s = s[:80] + "…"
			}
			return s
		}
	}
	return ""
}

// runAgentForCompare builds one agent over targetRoot and runs the prompt,
// capturing the tool chain (event bus) and token usage (observability metrics).
func runAgentForCompare(t *testing.T, cfg config.Config, client *llm.Client, provider, model string, budget config.TokenBudget, targetRoot string, withCodeIndex bool, prompt string) compareRunReport {
	t.Helper()
	name := "baseline"
	if withCodeIndex {
		name = "codeindex"
	}
	rep := compareRunReport{Name: name, CodeIndex: withCodeIndex}

	// Project docs come from the TARGET project (not the Lcoder repo) so neither
	// agent gets Lcoder-specific instructions while exploring Kocoro.
	contextText, _ := contextloader.NewLoader(targetRoot).Load()
	mgr := agentsetup.NewContextManager(cfg, budget, "", client, contextText, "", nil, nil)

	registry := tools.NewRegistry(targetRoot)
	if err := registry.RegisterBuiltinFactories(targetRoot); err != nil {
		rep.Error = fmt.Sprintf("register builtin tools: %v", err)
		return rep
	}

	ctx := context.Background()

	// Wire the code index exactly like cmd/lcoder/main.go:296-310, except the DB
	// lives in a temp dir (never write into reference/).
	if withCodeIndex {
		dbPath := filepath.Join(t.TempDir(), "codeindex.db")
		idx, err := sqlitestore.NewIndexer([]string{"go"}, cfg.CodeIndex.Exclude, dbPath)
		if err != nil {
			rep.Error = fmt.Sprintf("init code index: %v", err)
			return rep
		}
		defer idx.Close()

		start := time.Now()
		if err := idx.Update(ctx, targetRoot); err != nil {
			rep.Error = fmt.Sprintf("full index: %v", err)
			return rep
		}
		im := &indexMetrics{DBPath: dbPath, IndexLatencyMS: time.Since(start).Milliseconds()}
		if files, nodes, edges, unresolved, err := idx.Stats(ctx); err == nil {
			im.Files, im.Nodes, im.Edges, im.Unresolved = files, nodes, edges, unresolved
		}
		rep.Index = im

		injector := codeindex.NewInjector(idx, mgr, targetRoot, cfg.CodeIndex.MaxTokens)
		repoIndexTool := builtinTools.NewRepoIndex(targetRoot)
		repoIndexTool.SetInjector(injector)
		registry.Register("repo_index", repoIndexTool)
	}

	bus := events.New()
	var (
		mu        sync.Mutex
		chain     []toolLink
		pending   = map[string]int{} // toolCallID -> chain index
		turns     int
		lastReply string
	)
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch e := ev.(type) {
		case events.ToolExecutionStartEvent:
			mu.Lock()
			pending[e.ToolCallID] = len(chain)
			chain = append(chain, toolLink{Turn: e.Turn, Name: e.ToolName, KeyArg: keyArgOf(e.Args)})
			mu.Unlock()
		case events.ToolExecutionEndEvent:
			mu.Lock()
			if i, ok := pending[e.ToolCallID]; ok {
				chain[i].IsError = e.IsError
				delete(pending, e.ToolCallID)
			}
			mu.Unlock()
		case events.TurnEndEvent:
			mu.Lock()
			turns = e.Turn
			if text := e.Message.Text(); text != "" {
				lastReply = text
			}
			mu.Unlock()
		}
		return nil
	})

	exporter := observability.NewMemoryExporter()
	modeManager := agent.NewModeManager()
	ag, err := agent.NewBuilder().
		WithConfig(agent.Config{
			SystemPrompt:      "",
			Model:             models.ModelRef{Provider: provider, ID: model},
			ToolExecutionMode: models.ExecutionParallel,
			ContextManager:    mgr,
			ModeManager:       modeManager,
			DeferredTools:     false, // both agents see their full toolset
		}).
		WithGatewayClient(client).
		WithRegistry(registry).
		WithPermissions(permissions.NewEngineFromRules(nil)). // non-interactive: allow all
		WithEventBus(bus).
		WithObservability(observability.NewCollector(exporter)).
		Build()
	if err != nil {
		rep.Error = fmt.Sprintf("build agent: %v", err)
		return rep
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	start := time.Now()
	if err := ag.Prompt(runCtx, models.UserMessage(prompt)); err != nil {
		rep.Error = err.Error()
	}
	rep.WallTimeMS = time.Since(start).Milliseconds()

	mu.Lock()
	rep.Turns = turns
	rep.ToolChain = chain
	rep.FinalAnswer = lastReply
	mu.Unlock()

	// Sum token metrics recorded by the streamer via the observability collector.
	for _, r := range exporter.Records {
		if r.Metric == nil {
			continue
		}
		switch r.Metric.Name {
		case "llm_prompt_tokens":
			rep.PromptTokens += int64(r.Metric.Value)
		case "llm_completion_tokens":
			rep.CompletionTokens += int64(r.Metric.Value)
		case "llm_total_tokens":
			rep.TotalTokens += int64(r.Metric.Value)
		case "llm_cache_read_tokens":
			rep.CacheReadTokens += int64(r.Metric.Value)
		case "llm_cost_usd":
			rep.CostUSD += r.Metric.Value
		}
	}
	return rep
}

func TestCodeIndexCompare(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	client := llm.NewClient(buildEngineForTest(cfg))
	provider := selectProvider(cfg)
	if provider == "" {
		t.Skip("no provider with an API key configured; skipping codeindex comparison")
	}
	model := selectModel(cfg, client, provider)
	if model == "" {
		t.Skipf("no model resolvable for provider %q; set LCODER_IT_MODEL to override", provider)
	}

	ctx := context.Background()
	window, _ := client.ModelWindow(ctx, provider, model)
	maxOutput, _ := client.ModelMaxOutput(ctx, provider, model)
	budget, _ := cfg.ResolveContextBudget(window, maxOutput)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	targetRoot := filepath.Join(repoRoot, "reference", "Kocoro")
	if info, err := os.Stat(targetRoot); err != nil || !info.IsDir() {
		t.Skipf("reference/Kocoro not found at %s", targetRoot)
	}

	const prompt = "当前工作目录是 Kocoro 项目(一个用 Go 编写的终端 AI agent)。请分析它的 daemon 启动流程:从命令入口到主循环的函数调用链是怎样的?列出调用链上的关键函数及其所在文件。"

	report := compareReport{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Provider:    provider,
		Model:       model,
		Target:      targetRoot,
		Prompt:      prompt,
	}

	t.Log("=== run 1/2: baseline (no code index) ===")
	baseline := runAgentForCompare(t, cfg, client, provider, model, budget, targetRoot, false, prompt)
	report.Runs = append(report.Runs, baseline)

	t.Log("=== run 2/2: codeindex (repo_index tool wired) ===")
	withIndex := runAgentForCompare(t, cfg, client, provider, model, budget, targetRoot, true, prompt)
	report.Runs = append(report.Runs, withIndex)

	outDir := filepath.Join(wd, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	stamp := time.Now().Format("20060102_150405")
	jsonPath := filepath.Join(outDir, "codeindex_compare_"+stamp+".json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	mdPath := filepath.Join(outDir, "codeindex_compare_"+stamp+".md")
	if err := os.WriteFile(mdPath, []byte(renderCompareMarkdown(report)), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}

	for _, r := range report.Runs {
		t.Logf("[%s] turns=%d tools=%d prompt_tokens=%d completion_tokens=%d total_tokens=%d cost=$%.4f wall=%dms err=%q",
			r.Name, r.Turns, len(r.ToolChain), r.PromptTokens, r.CompletionTokens, r.TotalTokens, r.CostUSD, r.WallTimeMS, r.Error)
	}
	t.Logf("report written to %s", jsonPath)
	t.Logf("comparison written to %s", mdPath)

	for _, r := range report.Runs {
		if r.Error != "" {
			t.Errorf("run %s failed: %s", r.Name, r.Error)
		}
		if r.Turns == 0 {
			t.Errorf("run %s captured no turns", r.Name)
		}
	}
}

// renderCompareMarkdown produces the side-by-side human-readable comparison.
func renderCompareMarkdown(r compareReport) string {
	var b strings.Builder
	b.WriteString("# Code Index 接入对比报告\n\n")
	b.WriteString(fmt.Sprintf("- Generated: %s\n", r.GeneratedAt))
	b.WriteString(fmt.Sprintf("- Provider / Model: %s / %s\n", r.Provider, r.Model))
	b.WriteString(fmt.Sprintf("- Target: %s\n", r.Target))
	b.WriteString(fmt.Sprintf("- Prompt: %s\n\n", r.Prompt))

	b.WriteString("## 指标总览\n\n")
	b.WriteString("| 指标 | baseline(无索引) | codeindex(接入索引) |\n|------|------------------|---------------------|\n")
	var base, idx compareRunReport
	for _, run := range r.Runs {
		if run.CodeIndex {
			idx = run
		} else {
			base = run
		}
	}
	row := func(label string, f func(r compareRunReport) string) {
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", label, f(base), f(idx)))
	}
	row("轮数", func(r compareRunReport) string { return fmt.Sprintf("%d", r.Turns) })
	row("工具调用数", func(r compareRunReport) string { return fmt.Sprintf("%d", len(r.ToolChain)) })
	row("Prompt tokens(累计)", func(r compareRunReport) string { return fmt.Sprintf("%d", r.PromptTokens) })
	row("Completion tokens(累计)", func(r compareRunReport) string { return fmt.Sprintf("%d", r.CompletionTokens) })
	row("Total tokens(累计)", func(r compareRunReport) string { return fmt.Sprintf("%d", r.TotalTokens) })
	row("Cache read tokens", func(r compareRunReport) string { return fmt.Sprintf("%d", r.CacheReadTokens) })
	row("成本 (USD)", func(r compareRunReport) string { return fmt.Sprintf("%.4f", r.CostUSD) })
	row("耗时", func(r compareRunReport) string { return fmt.Sprintf("%dms", r.WallTimeMS) })
	row("错误", func(r compareRunReport) string {
		if r.Error == "" {
			return "无"
		}
		return r.Error
	})
	b.WriteString("\n")

	if idx.Index != nil {
		b.WriteString("## 索引构建指标\n\n")
		b.WriteString(fmt.Sprintf("- 全量索引耗时: %dms\n", idx.Index.IndexLatencyMS))
		b.WriteString(fmt.Sprintf("- 文件: %d | 节点: %d | 边: %d | 未解析: %d\n\n", idx.Index.Files, idx.Index.Nodes, idx.Index.Edges, idx.Index.Unresolved))
	}

	for _, run := range r.Runs {
		b.WriteString(fmt.Sprintf("## 工具调用链 — %s\n\n", run.Name))
		if len(run.ToolChain) == 0 {
			b.WriteString("_(无工具调用)_\n\n")
		} else {
			b.WriteString("| # | turn | tool | key arg | error |\n|---|------|------|---------|-------|\n")
			for i, link := range run.ToolChain {
				errMark := ""
				if link.IsError {
					errMark = "✗"
				}
				b.WriteString(fmt.Sprintf("| %d | %d | %s | %s | %s |\n", i+1, link.Turn, link.Name, link.KeyArg, errMark))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("### 最终回复 — %s\n\n```text\n%s\n```\n\n", run.Name, run.FinalAnswer))
	}
	return b.String()
}
