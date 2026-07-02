//go:build integration

// Package integration holds end-to-end tests that exercise the real agent loop
// against a real LLM provider, using the live configuration and API key from the
// current machine (~/.lcoder/config.yaml + credentials + env).
//
// These tests are gated behind the `integration` build tag so that the default
// `go test ./...` run and CI never touch the network or require credentials.
//
// Run with:
//
//	go test -tags integration ./test/integration/ -run TestAgentRealRun -v
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agentsetup"
	"github.com/lcoder/lcoder/pkg/config"
	contextloader "github.com/lcoder/lcoder/pkg/context"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	llmprovider "github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/tools"
	_ "github.com/lcoder/lcoder/pkg/tools/builtin" // registers builtin tool factories via init()
)

// toolCallRecord captures a single tool invocation observed on the event bus.
type toolCallRecord struct {
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
	Args       map[string]any `json:"args"`
	Result     string         `json:"result"`
	IsError    bool           `json:"is_error"`
}

// turnSnapshot is the structured state captured at the end of one agent turn.
type turnSnapshot struct {
	Turn         int                   `json:"turn"`
	Assistant    models.AgentMessage   `json:"assistant_message"`
	ToolResults  []models.AgentMessage `json:"tool_results"`
	ToolCalls    []toolCallRecord      `json:"tool_calls"`
	ContextStats map[string]int        `json:"context_stats"`
}

// runReport is the top-level structured output written to disk. It deliberately
// omits the API key and any provider credentials.
type runReport struct {
	GeneratedAt  string                 `json:"generated_at"`
	Provider     string                 `json:"provider"`
	Model        string                 `json:"model"`
	BudgetSource string                 `json:"budget_source"`
	Budget       contextmgr.TokenBudget `json:"budget"`
	Prompt       string                 `json:"prompt"`
	Turns        []turnSnapshot         `json:"turns"`
	FinalStats   map[string]int         `json:"final_context_stats"`
	Error        string                 `json:"error,omitempty"`
}

// buildEngineForTest replicates cmd/lcoder/main.go's buildEngine, which lives in
// package main and cannot be imported. It wires the catalog + engine and
// registers every provider connection from the resolved config.
func buildEngineForTest(cfg config.Config) *engine.Engine {
	cachePath := ""
	if home, err := os.UserHomeDir(); err == nil {
		cachePath = filepath.Join(home, ".lcoder", "cache", "models.json")
	}
	overrides := make([]catalog.Entry, 0, len(cfg.Catalog.Models))
	for _, m := range cfg.Catalog.Models {
		overrides = append(overrides, catalog.Entry{
			ID:            m.ID,
			Provider:      m.Provider,
			ContextWindow: m.ContextWindow,
			Capabilities:  m.Capabilities,
		})
	}
	cat := catalog.New(catalog.Options{Refresh: true, CachePath: cachePath, Overrides: overrides})
	eng := engine.New(cat)
	for name, conn := range cfg.Providers {
		eng.RegisterProvider(name, llmprovider.Conn{
			BaseURL: conn.BaseURL,
			APIKey:  conn.APIKey,
			Route:   conn.Route,
			Headers: conn.Headers,
		})
	}
	return eng
}

// selectProvider picks the provider to exercise. Priority:
//  1. LCODER_IT_PROVIDER env override
//  2. the configured cfg.Provider, if it has a resolvable key
//  3. the first provider in cfg.Providers (sorted) that carries an API key
//
// This lets the test honor whatever real credential is present (e.g. a single
// "deepseek" key) even when no config.yaml pins cfg.Provider to it.
func selectProvider(cfg config.Config) string {
	if v := os.Getenv("LCODER_IT_PROVIDER"); v != "" {
		return v
	}
	if cfg.Provider != "" && config.ProviderHasKey(cfg, cfg.Provider) {
		return cfg.Provider
	}
	names := make([]string, 0, len(cfg.Providers))
	for name, conn := range cfg.Providers {
		if conn.APIKey != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// selectModel picks a model for the chosen provider. Priority:
//  1. LCODER_IT_MODEL env override
//  2. cfg.Model, when the chosen provider is the configured one
//  3. the first tools-capable catalog model for that provider (any model as
//     fallback if none advertises the "tools" capability)
func selectModel(cfg config.Config, client *llm.Client, provider string) string {
	if v := os.Getenv("LCODER_IT_MODEL"); v != "" {
		return v
	}
	if provider == cfg.Provider && cfg.Model != "" {
		return cfg.Model
	}
	infos, err := client.ListModels(context.Background())
	if err != nil {
		return ""
	}
	var fallback string
	for _, mi := range infos {
		if !strings.EqualFold(mi.Provider, provider) {
			continue
		}
		if fallback == "" {
			fallback = mi.ID
		}
		for _, cap := range mi.Capabilities {
			if cap == "tools" {
				return mi.ID
			}
		}
	}
	return fallback
}

// renderMessageBody faithfully renders every content part of a message —
// plain text, tool calls (name + arguments), and tool results (payload) — so the
// markdown shows the real bytes the model exchanged, not just TextContent. The
// stock AgentMessage.Text() skips tool-call and tool-result parts, which would
// otherwise make tool turns look empty.
func renderMessageBody(msg models.AgentMessage) string {
	var parts []string
	for _, part := range msg.Content {
		switch p := part.(type) {
		case models.TextContent:
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		case models.ThinkingContent:
			if p.Text != "" {
				parts = append(parts, "[thinking] "+p.Text)
			}
		case models.ToolCallContent:
			args, _ := json.Marshal(p.Arguments)
			parts = append(parts, fmt.Sprintf("-> tool_call %s(%s)", p.Name, string(args)))
		case models.ToolResultContent:
			tag := "tool_result"
			if p.IsError {
				tag = "tool_result(ERROR)"
			}
			parts = append(parts, fmt.Sprintf("[%s %s]\n%s", tag, p.Name, p.Text()))
		}
	}
	if len(parts) == 0 {
		return "(无内容)"
	}
	return strings.Join(parts, "\n")
}

// renderPromptMarkdown turns the *actual* TurnRequest the agent sends to the
// model into a human-readable markdown document. It does not reconstruct or
// guess the prompt: req is whatever mgr.BuildTurnRequest produced, so the system
// prompt, message sequence, and tool list shown here are byte-for-byte what the
// model received. Sections follow the on-the-wire structure and order:
// system-prompt blocks (in canonical block order) -> merged system prompt ->
// messages -> tools -> token budget.
func renderPromptMarkdown(req models.TurnRequest, blocks []*contextmgr.Block, mgr *contextmgr.Manager, report runReport) string {
	var b strings.Builder

	b.WriteString("# Lcoder Agent —— 真实输入提示词快照\n\n")
	b.WriteString(fmt.Sprintf("- Generated: %s\n", report.GeneratedAt))
	b.WriteString(fmt.Sprintf("- Provider / Model: %s / %s\n", report.Provider, report.Model))
	b.WriteString(fmt.Sprintf("- Budget source: %s\n", report.BudgetSource))
	b.WriteString(fmt.Sprintf("- Turns executed: %d\n", len(report.Turns)))
	if report.Error != "" {
		b.WriteString(fmt.Sprintf("- Run error: %s\n", report.Error))
	}
	b.WriteString("\n本文件由集成测试在真实运行后,直接调用 `mgr.BuildTurnRequest(...)` 抓取——")
	b.WriteString("即模型实际收到的 system prompt、消息序列与工具定义,未经任何重构或猜测。\n\n")
	b.WriteString("---\n\n")

	// Section 1: system-prompt blocks, in canonical order. These are exactly the
	// blocks BuildTurnRequest joins (with "\n\n") into req.SystemPrompt.
	b.WriteString("## 一、System Prompt 分块结构(按 context manager 规范顺序)\n\n")
	b.WriteString("> 规范顺序 `system -> mode -> skills -> project_docs`。下列每个 system 类块依次用 `\\n\\n` 拼成最终 system prompt;非 system 块(recent 等)作为 messages 单独发送(见第三节)。\n\n")
	sysIdx := 0
	for _, blk := range blocks {
		if !contextmgr.IsSystemBlock(blk) {
			continue
		}
		text := blk.Text()
		if text == "" {
			continue
		}
		sysIdx++
		b.WriteString(fmt.Sprintf("### [%d] %s:%s\n", sysIdx, blk.Kind, blk.Name))
		b.WriteString(fmt.Sprintf("- stability: %v | priority: %d | tokens: ~%d\n\n", blk.Stability, blk.Priority, mgr.EstimateTokens(blk.Messages)))
		b.WriteString("```text\n")
		b.WriteString(text)
		b.WriteString("\n```\n\n")
	}
	if sysIdx == 0 {
		b.WriteString("_(无 system 块)_\n\n")
	}
	b.WriteString("---\n\n")

	// Section 2: the merged system prompt, verbatim — ground truth on the wire.
	b.WriteString("## 二、最终合并 System Prompt(逐字,模型实际收到)\n\n")
	b.WriteString("```text\n")
	b.WriteString(req.SystemPrompt)
	b.WriteString("\n```\n\n")
	b.WriteString("---\n\n")

	// Section 3: the message sequence (req.Messages = non-system blocks).
	b.WriteString(fmt.Sprintf("## 三、消息序列 Messages(共 %d 条)\n\n", len(req.Messages)))
	for i, msg := range req.Messages {
		header := fmt.Sprintf("### msg[%d] role=%s", i, msg.Role)
		if calls := msg.ToolCalls(); len(calls) > 0 {
			names := make([]string, 0, len(calls))
			for _, c := range calls {
				names = append(names, c.Name)
			}
			header += " (tool_calls: " + strings.Join(names, ", ") + ")"
		}
		b.WriteString(header + "\n\n")
		b.WriteString("```text\n")
		b.WriteString(renderMessageBody(msg))
		b.WriteString("\n```\n\n")
	}
	if len(req.Messages) == 0 {
		b.WriteString("_(无消息)_\n\n")
	}
	b.WriteString("---\n\n")

	// Section 4: tool definitions the model can call.
	b.WriteString(fmt.Sprintf("## 四、可用工具 Tools(共 %d 个)\n\n", len(req.Tools)))
	if len(req.Tools) > 0 {
		b.WriteString("| # | name | description |\n|---|------|-------------|\n")
		for i, tool := range req.Tools {
			desc := strings.ReplaceAll(tool.Description, "\n", " ")
			b.WriteString(fmt.Sprintf("| %d | %s | %s |\n", i+1, tool.Name, desc))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("_(无工具)_\n\n")
	}
	b.WriteString("---\n\n")

	// Section 5: token budget and per-block stats.
	b.WriteString("## 五、Token 预算与统计\n\n")
	stats := mgr.Stats()
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("| key | value |\n|-----|-------|\n")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("| %s | %d |\n", k, stats[k]))
	}
	if len(req.CacheBreakpoints) > 0 {
		marks := make([]string, 0, len(req.CacheBreakpoints))
		for _, bp := range req.CacheBreakpoints {
			marks = append(marks, fmt.Sprintf("%d", bp))
		}
		b.WriteString(fmt.Sprintf("| cache_breakpoints | %s |\n", strings.Join(marks, ", ")))
	}

	return b.String()
}

func TestAgentRealRun(t *testing.T) {
	// 1. Load the REAL config + credentials + env (the user's live setup).
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// 2. Engine + in-process client, exactly as the real binary wires them.
	client := llm.NewClient(buildEngineForTest(cfg))

	// 3. Resolve which provider/model to run against from the real credentials.
	provider := selectProvider(cfg)
	if provider == "" {
		t.Skip("no provider with an API key configured; skipping real-run integration test")
	}
	model := selectModel(cfg, client, provider)
	if model == "" {
		t.Skipf("no model resolvable for provider %q; set LCODER_IT_MODEL to override", provider)
	}

	// 4. Resolve the context budget from the discovered model window.
	ctx := context.Background()
	window, _ := client.ModelWindow(ctx, provider, model)
	maxOutput, _ := client.ModelMaxOutput(ctx, provider, model)
	cfgBudget, source := cfg.ResolveContextBudget(window, maxOutput)
	budget := contextmgr.TokenBudget{
		MaxTotal:         cfgBudget.MaxTotal,
		TargetTotal:      cfgBudget.TargetTotal,
		ReserveOutput:    cfgBudget.ReserveOutput,
		CompactThreshold: cfgBudget.CompactThreshold,
	}

	// 5. Compute repo root first so we can load project context and skills exactly
	// as cmd/lcoder/prepareAgent does.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))

	// Load project context and skills from the repo, mirroring prepareAgent.
	// Errors are non-fatal: missing files simply mean empty blocks.
	contextText, _ := contextloader.NewLoader(repoRoot).Load()
	loadedSkills, _ := skills.Load(skills.DefaultPaths(repoRoot))
	skillsBlock := skills.ToSystemPromptBlock(loadedSkills)

	// Context manager built exactly as the real binary builds it (shared
	// agentsetup.NewContextManager): same window policy, budget, and blocks. The
	// system prompt comes from agentsetup.BuildSystemPrompt — the same one
	// production uses; context/skills are injected as their own blocks (no
	// duplication into the system block).
	mgr := agentsetup.NewContextManager(cfg, cfgBudget, client, contextText, skillsBlock, nil)

	// Tool registry rooted at the repo so file tools can read go.mod.
	registry := tools.NewRegistry(repoRoot)
	if err := registry.RegisterBuiltinFactories(repoRoot); err != nil {
		t.Fatalf("register builtin tools: %v", err)
	}

	// 6. Event bus + capture handler for per-turn structured snapshots.
	bus := events.New()
	var (
		turns       []turnSnapshot
		pendingCall = map[string]*toolCallRecord{}
		turnCalls   []toolCallRecord
	)
	bus.Subscribe(func(_ context.Context, ev events.Event) error {
		switch e := ev.(type) {
		case events.ToolExecutionStartEvent:
			rec := &toolCallRecord{ToolCallID: e.ToolCallID, Name: e.ToolName, Args: e.Args}
			pendingCall[e.ToolCallID] = rec
		case events.ToolExecutionEndEvent:
			rec := pendingCall[e.ToolCallID]
			if rec == nil {
				rec = &toolCallRecord{ToolCallID: e.ToolCallID, Name: e.ToolName}
			}
			rec.IsError = e.IsError
			for _, part := range e.Result.Content {
				switch p := part.(type) {
				case models.TextContent:
					rec.Result += p.Text
				case models.ToolResultContent:
					rec.Result += p.Text()
				}
			}
			turnCalls = append(turnCalls, *rec)
			delete(pendingCall, e.ToolCallID)
		case events.TurnEndEvent:
			snap := turnSnapshot{
				Turn:         e.Turn,
				Assistant:    e.Message,
				ToolResults:  e.ToolResults,
				ToolCalls:    append([]toolCallRecord(nil), turnCalls...),
				ContextStats: mgr.Stats(),
			}
			turns = append(turns, snap)
			turnCalls = nil
		}
		return nil
	})

	// 7. Build the agent, mirroring cmd/lcoder prepareAgent's agent.Config:
	// same MaxTurns, parallel tool execution, "code" mode, and the default
	// natural-completion stop behavior (no custom ShouldStop — the loop now
	// keeps going while the model calls tools and stops on its first plain-text
	// answer). The one deliberate divergence is permissions: production wires an
	// interactive permission hook (BeforeToolCall) whose "ask" path blocks on
	// stdin and would hang a non-interactive test, so here we allow all tools.
	modeManager := agent.NewModeManager()
	_ = modeManager.LoadModes(agent.DefaultModeDirs(repoRoot))
	ag, err := agent.NewBuilder().
		WithConfig(agent.Config{
			SystemPrompt:      "",
			Model:             models.ModelRef{Provider: provider, ID: model},
			MaxTurns:          agentsetup.DefaultMaxTurns,
			ToolExecutionMode: models.ExecutionParallel,
			ContextManager:    mgr,
			Mode:              "code",
			ModeManager:       modeManager,
		}).
		WithGatewayClient(client).
		WithRegistry(registry).
		WithPermissions(permissions.NewEngineFromRules(nil)). // non-interactive: allow all (see note above)
		WithEventBus(bus).
		WithObservability(observability.NewCollector(observability.NewMemoryExporter())).
		Build()
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}

	// 8. A simple request that should drive at least one tool call.
	const prompt = "阅读当前的项目的结构，给出一个项目的总结报告"

	report := runReport{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Provider:     provider,
		Model:        model,
		BudgetSource: source,
		Budget:       budget,
		Prompt:       prompt,
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := ag.Prompt(runCtx, models.UserMessage(prompt)); err != nil {
		report.Error = err.Error()
	}
	report.Turns = turns
	report.FinalStats = mgr.Stats()

	// 9. Write the structured report to a file.
	outDir := filepath.Join(wd, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	outPath := filepath.Join(outDir, "agent_run_"+time.Now().Format("20060102_150405")+".json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	// 10. Also emit a markdown snapshot of the EXACT prompt the agent sent to the
	// model. We re-run the deterministic mgr.BuildTurnRequest with the same model
	// and tool set the loop used, so the captured request mirrors production wiring
	// (no TransformContext is configured, so the loop takes this same path).
	promptReq, prErr := mgr.BuildTurnRequest(models.ModelRef{Provider: provider, ID: model}, registry.Definitions())
	if prErr != nil {
		t.Fatalf("build turn request for markdown: %v", prErr)
	}
	md := renderPromptMarkdown(promptReq, mgr.Blocks(), mgr, report)
	mdPath := filepath.Join(outDir, "agent_prompt_"+time.Now().Format("20060102_150405")+".md")
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write prompt markdown: %v", err)
	}

	t.Logf("integration run complete: %d turns, report written to %s", len(turns), outPath)
	t.Logf("prompt markdown written to %s", mdPath)
	if report.Error != "" {
		t.Fatalf("agent run returned error (report still written): %s", report.Error)
	}
	if len(turns) == 0 {
		t.Fatal("no turns captured; expected at least one")
	}
}
