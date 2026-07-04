//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
)

// mechanismReport captures the before/after state of one compaction mechanism so
// the markdown visualization can show exactly what each path did to the messages.
type mechanismReport struct {
	Name         string   `json:"name"`
	Detail       string   `json:"detail"`
	Skipped      bool     `json:"skipped,omitempty"`
	Before       []string `json:"before"`
	After        []string `json:"after"`
	BeforeTokens int      `json:"before_tokens"`
	AfterTokens  int      `json:"after_tokens"`
	Verdict      string   `json:"verdict"`
}

// convo builds an alternating user/assistant conversation of n messages, each
// padded so DefaultEstimator (len/4) yields a non-trivial token count.
func convo(n int) []models.AgentMessage {
	msgs := make([]models.AgentMessage, 0, n)
	for i := 0; i < n; i++ {
		role := models.RoleUser
		if i%2 == 1 {
			role = models.RoleAssistant
		}
		text := fmt.Sprintf("message %d: %s", i, strings.Repeat("x", 200))
		msgs = append(msgs, models.NewAgentMessage(role, models.TextContent{Text: text}))
	}
	return msgs
}

// renderMsgs renders a message slice into per-message strings for the markdown.
func renderMsgs(msgs []models.AgentMessage) []string {
	out := make([]string, 0, len(msgs))
	for i, m := range msgs {
		out = append(out, fmt.Sprintf("[%d] role=%s | %s", i, m.Role, renderMessageBody(m)))
	}
	return out
}

// hasSummaryMessage reports whether any message is an injected compaction summary.
func hasSummaryMessage(msgs []models.AgentMessage) bool {
	for _, m := range msgs {
		if strings.Contains(m.Text(), "[Summary of earlier conversation]") {
			return true
		}
		if v, ok := m.Metadata["compacted"].(bool); ok && v {
			return true
		}
	}
	return false
}

func renderCompactionMarkdown(generatedAt, provider, model string, reports []mechanismReport) string {
	var b strings.Builder
	b.WriteString("# Lcoder 集成测试 —— 压缩机制验证\n\n")
	b.WriteString(fmt.Sprintf("- Generated: %s\n", generatedAt))
	b.WriteString(fmt.Sprintf("- Provider / Model(仅真实摘要子用例使用): %s / %s\n", provider, model))
	b.WriteString(fmt.Sprintf("- 机制数: %d\n\n", len(reports)))
	b.WriteString("逐一驱动 `compaction`、`contextmgr` 窗口策略截断与 `MaybeCompact` 已提交压缩的每条路径,展示压缩前后的消息序列与 token 变化。\n\n---\n\n")

	for i, r := range reports {
		b.WriteString(fmt.Sprintf("## %d. %s\n\n", i+1, r.Name))
		b.WriteString(r.Detail + "\n\n")
		if r.Skipped {
			b.WriteString("> 本子用例被跳过(无可用 provider)。\n\n---\n\n")
			continue
		}
		b.WriteString(fmt.Sprintf("- 压缩前: %d 条消息, ~%d tokens\n", len(r.Before), r.BeforeTokens))
		b.WriteString(fmt.Sprintf("- 压缩后: %d 条消息, ~%d tokens\n", len(r.After), r.AfterTokens))
		b.WriteString(fmt.Sprintf("- 结论: %s\n\n", r.Verdict))

		b.WriteString("<details><summary>压缩前消息</summary>\n\n```text\n")
		b.WriteString(strings.Join(r.Before, "\n"))
		b.WriteString("\n```\n\n</details>\n\n")

		b.WriteString("**压缩后消息:**\n\n```text\n")
		b.WriteString(strings.Join(r.After, "\n"))
		b.WriteString("\n```\n\n---\n\n")
	}
	return b.String()
}

func TestCompactionMechanisms(t *testing.T) {
	// Provider is needed only for the real-LLM summarizer sub-case; the rest run
	// deterministically without any network.
	cfg, _ := config.Load()
	var client *llm.Client
	var provider, model string
	if c := llm.NewClient(buildEngineForTest(cfg)); c != nil {
		client = c
		provider = selectProvider(cfg)
		if provider != "" {
			model = selectModel(cfg, client, provider)
		}
	}

	const testModelID = "test-model"
	var reports []mechanismReport

	// 1. SimpleSummarize + KeepRecent strategy (deterministic placeholder).
	t.Run("SimpleSummarize_KeepRecent", func(t *testing.T) {
		before := convo(12)
		const keep = 4
		after, err := compaction.NewKeepLastStrategy(keep).Compact(before, compaction.SimpleSummarize)
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if len(after) != keep+1 {
			t.Fatalf("expected %d messages after compaction, got %d", keep+1, len(after))
		}
		if after[0].Role != models.RoleSystem {
			t.Fatalf("expected first message to be the system summary, got role %s", after[0].Role)
		}
		if v, ok := after[0].Metadata["compacted"].(bool); !ok || !v {
			t.Fatalf("expected summary message to carry metadata compacted=true")
		}
		reports = append(reports, mechanismReport{
			Name:         "SimpleSummarize + KeepRecent",
			Detail:       "`compaction.KeepRecent.Compact`:把最后 N 条以外的旧消息交给 `SimpleSummarize` 生成占位摘要,作为一条 `compacted=true` 的 system 消息前置。",
			Before:       renderMsgs(before),
			After:        renderMsgs(after),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(after),
			Verdict:      fmt.Sprintf("PASS —— 12 条压缩为 %d 条(1 摘要 + %d 近期),首条带 compacted 元数据", len(after), keep),
		})
	})

	// 2. Window policy truncation with NO summarizer.
	t.Run("WindowPolicy_Truncation_NoSummarizer", func(t *testing.T) {
		budget := contextmgr.TokenBudget{MaxTotal: 110, TargetTotal: 90, ReserveOutput: 50}
		mgr := contextmgr.NewManager(budget,
			contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(2)))
		mgr.SetSystemPrompt("you are a test agent")
		before := convo(20)
		mgr.ReplaceRecent(before)

		req, err := mgr.BuildTurnRequest(models.ModelRef{ID: testModelID}, nil)
		if err != nil {
			t.Fatalf("build turn request: %v", err)
		}
		if len(req.Messages) == 0 || len(req.Messages) >= len(before) {
			t.Fatalf("expected truncation to a tail; got %d of %d", len(req.Messages), len(before))
		}
		// The tail must end at the last original message (kept from the end).
		if req.Messages[len(req.Messages)-1].Text() != before[len(before)-1].Text() {
			t.Fatalf("expected truncated tail to retain the final message")
		}
		if got := contextmgr.DefaultEstimator(req.Messages); got > budget.EffectiveInput() {
			t.Fatalf("truncated messages ~%d tokens exceed effective input %d", got, budget.EffectiveInput())
		}
		reports = append(reports, mechanismReport{
			Name:         "WindowPolicy 截断(无 summarizer)",
			Detail:       "`KeepRecentInBudget.fitWithoutCompaction` + `keepTail`:无摘要器时,动态块只能从尾部截断到预算内,保留最近消息。",
			Before:       renderMsgs(before),
			After:        renderMsgs(req.Messages),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(req.Messages),
			Verdict:      fmt.Sprintf("PASS —— 20 条截断为 %d 条尾部消息,~%d tokens 在 EffectiveInput=%d 内", len(req.Messages), contextmgr.DefaultEstimator(req.Messages), budget.EffectiveInput()),
		})
	})

	// 3. Eager compaction committed via MaybeCompact with a (deterministic) summarizer.
	t.Run("MaybeCompact_EagerCompaction_SimpleSummarizer", func(t *testing.T) {
		budget := contextmgr.TokenBudget{MaxTotal: 1200, TargetTotal: 150, ReserveOutput: 50}
		mgr := contextmgr.NewManager(budget,
			contextmgr.WithSummarizer(contextmgr.SummarizeFunc(compaction.SimpleSummarize)),
			contextmgr.WithMinRecent(2))
		mgr.SetSystemPrompt("you are a test agent")
		before := convo(20)
		mgr.ReplaceRecent(before)

		committed, err := mgr.MaybeCompact()
		if err != nil {
			t.Fatalf("MaybeCompact: %v", err)
		}
		if !committed {
			t.Fatalf("expected MaybeCompact to commit when total > CompactLimit")
		}
		recent, ok := mgr.GetBlock(contextmgr.BlockRecent, "recent")
		if !ok {
			t.Fatalf("recent block missing after compaction")
		}
		after := recent.Messages
		if !hasSummaryMessage(after) {
			t.Fatalf("expected an injected compaction summary in recent block")
		}
		if len(after) >= len(before) {
			t.Fatalf("expected compaction to shrink the message count; got %d of %d", len(after), len(before))
		}
		if v, ok := after[0].Metadata["compacted"].(bool); !ok || !v {
			t.Fatalf("expected folded summary at head with metadata compacted=true")
		}
		reports = append(reports, mechanismReport{
			Name:         "MaybeCompact 急切压缩(已提交,注入摘要)",
			Detail:       "`Manager.MaybeCompact`:turn 边界检查 `total > CompactLimit` 时触发,把旧消息交给摘要器生成 `[Summary of earlier conversation]`,作为 `compacted=true` 的 system 消息前置回写 recent 块(原地提交,非 BuildTurnRequest 时临时计算)。",
			Before:       renderMsgs(before),
			After:        renderMsgs(after),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(after),
			Verdict:      fmt.Sprintf("PASS —— 20 条折叠为 %d 条(1 摘要 + 近期尾部),已原地提交", len(after)),
		})
	})

	// 4. Leading orphan tool_result stripping (indirect, via truncation).
	t.Run("StripLeadingOrphanToolResults", func(t *testing.T) {
		big := func(role models.MessageRole) models.AgentMessage {
			return models.NewAgentMessage(role, models.TextContent{Text: strings.Repeat("y", 400)})
		}
		toolRes := func(label string) models.AgentMessage {
			return models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
				ToolCallID: label, Name: "probe",
				Content: []models.ContentPart{models.TextContent{Text: "result " + strings.Repeat("z", 60)}},
			})
		}
		// Tail order: two big messages (cut off), then orphan tool_results, then a
		// small user. Truncation lands inside the tool_results, so the kept tail
		// would START with an orphan tool_result unless stripped.
		before := []models.AgentMessage{
			big(models.RoleUser),
			big(models.RoleAssistant),
			toolRes("call-1"),
			toolRes("call-2"),
			models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "final small user"}),
		}
		budget := contextmgr.TokenBudget{MaxTotal: 110, TargetTotal: 90, ReserveOutput: 50}
		mgr := contextmgr.NewManager(budget,
			contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(2)))
		mgr.SetSystemPrompt("sys")
		mgr.ReplaceRecent(before)

		req, err := mgr.BuildTurnRequest(models.ModelRef{ID: testModelID}, nil)
		if err != nil {
			t.Fatalf("build turn request: %v", err)
		}
		if len(req.Messages) == 0 {
			t.Fatalf("expected a non-empty tail")
		}
		if req.Messages[0].Role == models.RoleToolResult {
			t.Fatalf("tail must not start with an orphan tool_result")
		}
		reports = append(reports, mechanismReport{
			Name:         "孤儿 tool_result 剥离",
			Detail:       "`stripLeadingOrphanToolResults`:截断/压缩后的 tail 若以 tool_result 开头,其配对的 tool_use 已被切掉(会被 Anthropic 以 400 拒绝),故剥离开头连续的 tool_result。",
			Before:       renderMsgs(before),
			After:        renderMsgs(req.Messages),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(req.Messages),
			Verdict:      fmt.Sprintf("PASS —— tail 首条 role=%s,非孤儿 tool_result", req.Messages[0].Role),
		})
	})

	// 5. Circuit breaker: trips after repeated failures; manager degrades gracefully.
	t.Run("CircuitBreaker_Fallback", func(t *testing.T) {
		// (a) Direct breaker behavior.
		cb := compaction.NewCircuitBreaker(0) // default max = 3
		boom := errors.New("boom")
		failing := compaction.SummarizeFunc(func(_ []models.AgentMessage) (string, error) { return "", boom })
		wrapped := cb.Wrap(failing)
		for i := 0; i < 3; i++ {
			if _, err := wrapped(nil); !errors.Is(err, boom) {
				t.Fatalf("call %d: expected inner error, got %v", i+1, err)
			}
		}
		if _, err := wrapped(nil); !errors.Is(err, compaction.ErrCompactionSkipped) {
			t.Fatalf("expected ErrCompactionSkipped once breaker is open, got %v", err)
		}

		// (b) summarizer 失败必须非致命:MaybeCompact 把错误原样返回且不 mutate 状态;
		// BuildTurnRequest 与压缩解耦,无论摘要成败都只截断、不注入摘要、不报错。
		budget := contextmgr.TokenBudget{MaxTotal: 300, TargetTotal: 150, ReserveOutput: 50}
		mgr := contextmgr.NewManager(budget,
			contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(2)),
			contextmgr.WithMinRecent(2),
			contextmgr.WithSummarizer(contextmgr.SummarizeFunc(func(_ []models.AgentMessage) (string, error) {
				return "", boom
			})))
		mgr.SetSystemPrompt("you are a test agent")
		before := convo(20)
		mgr.ReplaceRecent(before)

		if _, err := mgr.MaybeCompact(); !errors.Is(err, boom) {
			t.Fatalf("expected MaybeCompact to surface the summarizer error, got %v", err)
		}
		req, err := mgr.BuildTurnRequest(models.ModelRef{ID: testModelID}, nil)
		if err != nil {
			t.Fatalf("build turn request must not fail on summarizer error: %v", err)
		}
		if hasSummaryMessage(req.Messages) {
			t.Fatalf("expected no summary injected when summarizer fails")
		}
		if len(req.Messages) == 0 {
			t.Fatalf("expected a kept tail when compaction is skipped")
		}
		reports = append(reports, mechanismReport{
			Name:         "CircuitBreaker 降级",
			Detail:       "`CircuitBreaker.Wrap` 连续 3 次失败后返回 `ErrCompactionSkipped`;装进 Manager 后,`MaybeCompact` 把摘要错误作为非致命 error 返回且不改动状态,agent loop 视为非致命继续。`BuildTurnRequest` 与压缩解耦,失败时仅截断、不注入摘要、不报错。",
			Before:       renderMsgs(before),
			After:        renderMsgs(req.Messages),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(req.Messages),
			Verdict:      fmt.Sprintf("PASS —— 断路器 3 次后开路;摘要失败时 MaybeCompact 返回错误且不 mutate,BuildTurnRequest 仍截断为 %d 条尾部、无摘要、无错误", len(req.Messages)),
		})
	})

	// 6. Real LLM summarizer eager compaction (gated on a live provider).
	t.Run("RealLLMSummarizer_EagerCompaction", func(t *testing.T) {
		if provider == "" || model == "" {
			reports = append(reports, mechanismReport{
				Name:    "真实 LLM 摘要急切压缩",
				Detail:  "`compaction.NewLLMSummarizer`:用真实 provider 把旧消息压缩为双段 `<summary>`,注入 recent。",
				Skipped: true,
				Verdict: "SKIP —— 无可用 provider/key",
			})
			t.Skip("no provider with an API key configured; skipping real-LLM summarizer sub-case")
		}

		budget := contextmgr.TokenBudget{MaxTotal: 500, TargetTotal: 250, ReserveOutput: 50}
		breaker := compaction.NewCircuitBreaker(0)
		summarizer := compaction.NewLLMSummarizer(client, models.ModelRef{Provider: provider, ID: model})
		mgr := contextmgr.NewManager(budget,
			contextmgr.WithSummarizer(contextmgr.SummarizeFunc(breaker.Wrap(summarizer))),
			contextmgr.WithMinRecent(2))
		mgr.SetSystemPrompt("you are a test agent")

		before := []models.AgentMessage{
			models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "我们要给 Lcoder 加一个 retry 机制:LLM 流式请求失败时最多重试 3 次,指数退避从 1s 起。" + strings.Repeat("。", 80)}),
			models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "明白。我打算在 pkg/llm/client.go 的 StreamTurn 外面包一层 retryStream,失败时按 1s/2s/4s 退避重试。" + strings.Repeat("。", 80)}),
			models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "注意只对网络错误和 5xx 重试,4xx 直接返回。" + strings.Repeat("。", 80)}),
			models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "好的,我会用 errors.As 判断错误类型,4xx 不重试。已经写好 retryStream 并加了单测。" + strings.Repeat("。", 80)}),
			models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "再确认一下退避上限和 jitter。" + strings.Repeat("。", 80)}),
			models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "退避上限 30s,加 ±20% jitter 防止惊群。" + strings.Repeat("。", 80)}),
			models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "现在帮我把这个机制接到 summarizer 的调用上。" + strings.Repeat("。", 80)}),
		}
		mgr.ReplaceRecent(before)

		committed, err := mgr.MaybeCompact()
		if err != nil {
			t.Fatalf("MaybeCompact: %v", err)
		}
		if !committed {
			t.Fatalf("expected real-LLM compaction to commit when total > CompactLimit")
		}
		recent, ok := mgr.GetBlock(contextmgr.BlockRecent, "recent")
		if !ok {
			t.Fatalf("recent block missing after compaction")
		}
		after := recent.Messages
		if !hasSummaryMessage(after) {
			t.Fatalf("expected an injected real-LLM summary in recent block")
		}
		// Capture the actual summary text for the visualization.
		var summaryText string
		for _, m := range after {
			if strings.Contains(m.Text(), "[Summary of earlier conversation]") {
				summaryText = m.Text()
				break
			}
		}
		if len(strings.TrimSpace(summaryText)) < len("[Summary of earlier conversation]")+20 {
			t.Fatalf("expected a non-trivial real summary, got %q", summaryText)
		}
		reports = append(reports, mechanismReport{
			Name:         "真实 LLM 摘要急切压缩",
			Detail:       "`compaction.NewLLMSummarizer`(经 CircuitBreaker 包裹):`Manager.MaybeCompact` 用真实 provider 把旧消息压缩为双段提示中的 `<summary>`,作为 `compacted=true` 的 system 消息原地提交进 recent 块。",
			Before:       renderMsgs(before),
			After:        renderMsgs(after),
			BeforeTokens: contextmgr.DefaultEstimator(before),
			AfterTokens:  contextmgr.DefaultEstimator(after),
			Verdict:      fmt.Sprintf("PASS —— 真实模型生成 %d 字摘要并原地提交", len([]rune(summaryText))),
		})
	})

	// Write the combined visualization (JSON + markdown).
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	outDir := filepath.Join(wd, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	generatedAt := time.Now().Format(time.RFC3339)
	ts := time.Now().Format("20060102_150405")
	if data, err := json.MarshalIndent(reports, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(outDir, "compaction_"+ts+".json"), data, 0o644)
	}
	md := renderCompactionMarkdown(generatedAt, provider, model, reports)
	mdPath := filepath.Join(outDir, "compaction_"+ts+".md")
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	t.Logf("compaction mechanisms verified: %d reports, markdown at %s", len(reports), mdPath)
}
