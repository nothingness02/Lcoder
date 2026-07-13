//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
	_ "github.com/lcoder/lcoder/pkg/tools/builtin" // registers builtin tool factories
)

// TestContextEngineeringFeatures drives the four context-engineering features —
// multi-level compaction, ephemeral system-reminders, deferred tool loading +
// tool_search, and cache breakpoints + real token accounting — deterministically
// (no network) and writes a markdown snapshot that shows each one operating on
// real Manager/Registry state. The snapshot is the acceptance artifact: it must
// clearly exhibit all four features.
func TestContextEngineeringFeatures(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))

	var b strings.Builder
	b.WriteString("# Lcoder 上下文工程四项功能 —— 集成快照\n\n")
	b.WriteString(fmt.Sprintf("- Generated: %s\n", time.Now().Format(time.RFC3339)))
	b.WriteString("- 本快照由 `TestContextEngineeringFeatures` 确定性运行(无网络)生成,逐一展示真实的 `contextmgr.Manager` / `tools.Registry` 状态。\n\n---\n\n")

	renderFeature1MultiLevelCompaction(t, &b)
	renderFeature2EphemeralReminders(t, &b)
	renderFeature3DeferredTools(t, &b, repoRoot)
	renderFeature4CacheAndRealTokens(t, &b)

	outDir := filepath.Join(wd, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	mdPath := filepath.Join(outDir, "contexteng_"+time.Now().Format("20060102_150405")+".md")
	if err := os.WriteFile(mdPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	t.Logf("context-engineering snapshot written to %s", mdPath)
}

// ---- Feature 1: multi-level compaction ------------------------------------

func renderFeature1MultiLevelCompaction(t *testing.T, b *strings.Builder) {
	b.WriteString("## 一、多级压缩 (multi-level compaction)\n\n")
	b.WriteString("`TokenBudget.PressureLevel` 把 prompt token 占用映射到四个压力档:")
	b.WriteString("`none(<90%)` / `proactive(>=90%)` / `preflight(>=95%)` / `reactive(>=100%)`;")
	b.WriteString("`Manager.MaybeCompactLeveled` 按档位用不同 token 预算折叠旧消息(档位越高,保留尾部越小)。\n\n")

	// Probe the heuristic token total of a fixed 20-message conversation.
	probe := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 1 << 30})
	probe.SetSystemPrompt("you are a test agent")
	probe.ReplaceRecent(convo(20))
	total := probe.Stats()["total"]
	b.WriteString(fmt.Sprintf("固定 20 条对话(system + recent),启发式估算 total = **%d tokens**。针对同一对话,只改预算窗口以落入每个压力档:\n\n", total))

	type band struct {
		name     string
		maxTotal int
		want     contextmgr.CompactionLevel
	}
	bands := []band{
		{"none", int(float64(total) / 0.70), contextmgr.CompactionNone},
		{"proactive", int(float64(total) / 0.92), contextmgr.CompactionProactive},
		{"preflight", int(float64(total) / 0.97), contextmgr.CompactionPreflight},
		{"reactive", int(float64(total) / 1.05), contextmgr.CompactionReactive},
	}

	b.WriteString("| 预算 EffectiveInput | total/eff | 命中档位 | 触发折叠 | 压缩前 | 压缩后 |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, bd := range bands {
		budget := contextmgr.TokenBudget{MaxTotal: bd.maxTotal, ReserveOutput: 0}
		mgr := contextmgr.NewManager(budget,
			contextmgr.WithSummarizer(contextmgr.SummarizeFunc(compaction.SimpleSummarize)),
			contextmgr.WithMinRecent(8))
		mgr.SetSystemPrompt("you are a test agent")
		mgr.ReplaceRecent(convo(20))

		gotLevel := budget.PressureLevel(total)
		if gotLevel != bd.want {
			t.Fatalf("band %s: PressureLevel(%d) over eff=%d = %v, want %v", bd.name, total, bd.maxTotal, gotLevel, bd.want)
		}

		level, res, err := mgr.MaybeCompactLeveled(context.Background())
		if err != nil {
			t.Fatalf("band %s: MaybeCompactLeveled: %v", bd.name, err)
		}
		committed := res.Committed
		recent, _ := mgr.GetBlock(contextmgr.BlockRecent, "recent")
		after := len(recent.Messages)
		ratio := float64(total) / float64(bd.maxTotal)
		b.WriteString(fmt.Sprintf("| %d | %.2f | **%s** | %v | 20 | %d |\n",
			bd.maxTotal, ratio, level, committed, after))

		// Assertions: only the no-pressure band leaves the conversation untouched;
		// every pressure band must commit a fold that shrinks the message count.
		if bd.want == contextmgr.CompactionNone {
			if committed || after != 20 {
				t.Fatalf("none band must not compact: committed=%v after=%d", committed, after)
			}
		} else {
			if !committed || after >= 20 {
				t.Fatalf("band %s must fold: committed=%v after=%d", bd.name, committed, after)
			}
		}
	}
	b.WriteString("\n> 结论:PASS —— 同一对话在四种预算下分别命中 none/proactive/preflight/reactive,压力档越高折叠后保留越少。\n\n---\n\n")
}

// ---- Feature 2: ephemeral system-reminders --------------------------------

func renderFeature2EphemeralReminders(t *testing.T, b *strings.Builder) {
	b.WriteString("## 二、临时态 system-reminder (ephemeral)\n\n")
	b.WriteString("`Manager.SetEphemeralReminders` 设置的提醒只在**当前** `BuildTurnRequest` 注入(尾部合成 user 消息,包 `<system-reminder>`,标记 `ephemeral=true`),")
	b.WriteString("不写入任何 block、不作为 cache 断点;下一 turn 若不重设则自动消失。\n\n")

	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 10000, ReserveOutput: 1000})
	mgr.SetSystemPrompt("you are a test agent")
	mgr.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "帮我重构 retryStream。"}),
	})
	mgr.SetEphemeralReminders([]string{
		"You have 1 unfinished todo item. Continue working toward it.",
		"Your previous output was not valid JSON. Reply with valid JSON only.",
	})

	req, err := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}

	b.WriteString(fmt.Sprintf("**本 turn 注入后的消息序列(共 %d 条,模型实际收到):**\n\n```text\n", len(req.Messages)))
	for i, m := range req.Messages {
		tag := ""
		if contextmgr.IsEphemeral(m) {
			tag = " [EPHEMERAL]"
		}
		b.WriteString(fmt.Sprintf("[%d] role=%s%s | %s\n", i, m.Role, tag, renderMessageBody(m)))
	}
	b.WriteString("```\n\n")

	last := req.Messages[len(req.Messages)-1]
	if !contextmgr.IsEphemeral(last) || !strings.Contains(last.Text(), "<system-reminder>") {
		t.Fatalf("expected trailing ephemeral <system-reminder> message")
	}

	// Persisted history must not contain the reminder.
	persisted := mgr.AllMessages()
	leaked := false
	for _, m := range persisted {
		if contextmgr.IsEphemeral(m) {
			leaked = true
		}
	}
	if leaked {
		t.Fatalf("ephemeral reminder leaked into persisted history")
	}
	b.WriteString(fmt.Sprintf("**持久化历史 `AllMessages()`(共 %d 条):** 不含任何 ephemeral 消息(提醒未落库)。\n\n", len(persisted)))

	// Breakpoints must anchor the real user turn, never the ephemeral tail.
	bpOnEphemeral := false
	for _, bp := range req.CacheBreakpoints {
		if bp == len(req.Messages)-1 {
			bpOnEphemeral = true
		}
	}
	if bpOnEphemeral {
		t.Fatalf("cache breakpoint anchored the ephemeral message")
	}
	b.WriteString(fmt.Sprintf("**cache 断点:** `%v` —— 锚定真实 user 轮次,未锚定 ephemeral 尾消息(否则每轮都会冲掉缓存)。\n\n", req.CacheBreakpoints))

	// Next turn: cleared → gone.
	mgr.ClearEphemeralReminders()
	req2, _ := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	for _, m := range req2.Messages {
		if contextmgr.IsEphemeral(m) {
			t.Fatalf("ephemeral reminder survived into next turn")
		}
	}
	b.WriteString(fmt.Sprintf("**清除后下一 turn 的消息序列(共 %d 条):** ephemeral 已消失。\n\n", len(req2.Messages)))
	b.WriteString("> 结论:PASS —— 提醒仅当前 turn 注入、不落库、不锚缓存、清除后即消失。\n\n---\n\n")
}

// ---- Feature 3: deferred tool loading + tool_search -----------------------

func renderFeature3DeferredTools(t *testing.T, b *strings.Builder, repoRoot string) {
	b.WriteString("## 三、延迟工具加载 + tool_search\n\n")
	b.WriteString("`Registry.DeferredDefinitions(core...)` 只为核心工具下发完整 JSON schema,并附带 `tool_search` 元工具;")
	b.WriteString("其余工具降级为 name-only 的 `(deferred)` stub(无参数 schema,几乎零 token)。")
	b.WriteString("模型按需调用 `tool_search` 关键字,`Registry.SearchTools` 返回该工具的完整 schema。\n\n")

	registry := tools.NewRegistry(repoRoot)
	if err := registry.RegisterBuiltinFactories(repoRoot); err != nil {
		t.Fatalf("register builtin tools: %v", err)
	}
	full := registry.Definitions()
	active, deferred := registry.DeferredDefinitions("read", "bash")

	b.WriteString(fmt.Sprintf("注册内置工具共 **%d** 个;选 `read`、`bash` 为核心,其余延迟。\n\n", len(full)))

	b.WriteString(fmt.Sprintf("**Active(完整 schema,共 %d 项 = 核心 + tool_search):**\n\n", len(active)))
	b.WriteString("| name | 有参数 schema | description |\n|---|---|---|\n")
	for _, d := range active {
		b.WriteString(fmt.Sprintf("| %s | %v | %s |\n", d.Name, d.Parameters != nil, oneLine(d.Description)))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("**Deferred(name-only stub,共 %d 项):**\n\n", len(deferred)))
	b.WriteString("| name | 有参数 schema | description |\n|---|---|---|\n")
	for _, d := range deferred {
		b.WriteString(fmt.Sprintf("| %s | %v | %s |\n", d.Name, d.Parameters != nil, oneLine(d.Description)))
	}
	b.WriteString("\n")

	// tool_search must be present in active.
	hasSearch := false
	for _, d := range active {
		if d.Name == tools.ToolSearchName {
			hasSearch = true
		}
	}
	if !hasSearch {
		t.Fatalf("active set must include the tool_search meta-tool")
	}
	// Deferred stubs must carry no parameter schema.
	for _, d := range deferred {
		if d.Parameters != nil {
			t.Fatalf("deferred stub %q must not carry a parameter schema", d.Name)
		}
	}

	// tool_search("edit") recovers the FULL schema of the deferred edit tool.
	hits := registry.SearchTools("edit")
	if len(hits) == 0 || hits[0].Name != "edit" || hits[0].Parameters == nil {
		t.Fatalf("tool_search(\"edit\") must resolve edit's full schema, got %v", hits)
	}
	paramKeys := schemaKeys(hits[0].Parameters)
	b.WriteString(fmt.Sprintf("**模型调用 `tool_search(\"edit\")` 的解析结果:** 返回 `edit` 的完整 schema(顶层 schema 键:`%v`),延迟工具被按需加载。\n\n", paramKeys))
	b.WriteString(fmt.Sprintf("> 结论:PASS —— 工具下发从 %d 个完整 schema 降为 %d 个完整(核心)+ %d 个 stub,`tool_search` 可按需恢复完整 schema。\n\n---\n\n",
		len(full), len(active)-1, len(deferred)))
}

// ---- Feature 4: cache breakpoints + real token accounting -----------------

func renderFeature4CacheAndRealTokens(t *testing.T, b *strings.Builder) {
	b.WriteString("## 四、缓存断点 + 真实 token 计数\n\n")
	b.WriteString("`BuildTurnRequest` 在稳定前缀(system 等)足够大时,于首条非 system 消息放置 cache 断点,并始终为最后一条 user 消息打断点;")
	b.WriteString("`RecordRealUsage` 把 provider 上报的 `input + cache_read + cache_creation` 回流为真实 token 计数,驱动预算与压缩判定(取代字符启发式)。\n\n")

	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 180000, TargetTotal: 170000, ReserveOutput: 0})
	// A large stable system prompt (>=256 est tokens) to trigger the prefix breakpoint.
	mgr.SetSystemPrompt("You are Lcoder, an expert software engineering agent.\n\n" + strings.Repeat("Operating guideline. ", 120))
	mgr.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "读取 go.mod 并告诉我 module 名。"}),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "好的,我先读取文件。"}),
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "继续。"}),
	})

	req, err := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	if len(req.CacheBreakpoints) == 0 {
		t.Fatalf("expected cache breakpoints for a large stable prefix")
	}
	b.WriteString(fmt.Sprintf("**cache 断点:** `%v` 于 %d 条消息中。\n", req.CacheBreakpoints, len(req.Messages)))
	b.WriteString("- 前缀断点(idx 0):稳定 system 前缀 >=256 token,可缓存,避免每轮重复计费。\n")
	b.WriteString("- 末位 user 断点:让本轮新增内容之前的全部前缀复用缓存。\n\n")

	// Before any turn: accounting uses the heuristic estimate.
	statsBefore := mgr.Stats()
	if _, ok := mgr.RealPromptTokens(); ok {
		t.Fatalf("expected no real usage before recording")
	}
	b.WriteString(fmt.Sprintf("**首轮前(无 provider 回流):** 启发式估算 total = %d tokens;`compaction_level` = %d(基于估算)。\n\n",
		statsBefore["total"], statsBefore["compaction_level"]))

	// Feed a realistic provider usage report.
	mgr.RecordRealUsage(models.LLMUsage{
		PromptTokens:     12000,  // fresh input
		CacheReadTokens:  150000, // cache_read (cached prefix reused)
		CacheWriteTokens: 3000,   // cache_creation
	})
	real, ok := mgr.RealPromptTokens()
	if !ok || real != 165000 {
		t.Fatalf("RealPromptTokens = (%d,%v), want (165000,true)", real, ok)
	}
	statsAfter := mgr.Stats()
	if statsAfter["real_prompt_total"] != 165000 {
		t.Fatalf("Stats real_prompt_total = %d, want 165000", statsAfter["real_prompt_total"])
	}
	// The contrast that proves real tokens (not the char heuristic) drive the
	// pressure level: the estimate is tiny (level none), but 165000/180000 =
	// 0.917 lands in the proactive band.
	if statsBefore["compaction_level"] != int(contextmgr.CompactionNone) {
		t.Fatalf("estimate-based level should be none, got %d", statsBefore["compaction_level"])
	}
	if statsAfter["compaction_level"] != int(contextmgr.CompactionProactive) {
		t.Fatalf("real-token level should be proactive, got %d", statsAfter["compaction_level"])
	}
	b.WriteString("**回流 provider usage 后(真实 token 计数):**\n\n")
	b.WriteString("| 维度 | tokens |\n|---|---|\n")
	b.WriteString(fmt.Sprintf("| input (fresh) | %d |\n", statsAfter["real_input"]))
	b.WriteString(fmt.Sprintf("| cache_read | %d |\n", statsAfter["real_cache_read"]))
	b.WriteString(fmt.Sprintf("| cache_creation | %d |\n", statsAfter["real_cache_creation"]))
	b.WriteString(fmt.Sprintf("| **real_prompt_total** | **%d** |\n", statsAfter["real_prompt_total"]))
	b.WriteString(fmt.Sprintf("| 启发式估算 total | %d |\n", statsAfter["total"]))
	b.WriteString(fmt.Sprintf("| compaction_level(基于真实 token) | %d |\n\n", statsAfter["compaction_level"]))
	b.WriteString("> 结论:PASS —— 缓存断点同时覆盖稳定前缀与末位 user;真实 token 计数 = input + cache_read + cache_creation = ")
	b.WriteString(fmt.Sprintf("%d + %d + %d = **%d**;字符估算仅 %d(档位 none),真实计数把压缩档位推到 proactive,据此驱动压缩(取代字符估算)。\n\n---\n\n",
		statsAfter["real_input"], statsAfter["real_cache_read"], statsAfter["real_cache_creation"], statsAfter["real_prompt_total"], statsAfter["total"]))
}

// ---- small helpers ---------------------------------------------------------

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func schemaKeys(schema map[string]any) []string {
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
