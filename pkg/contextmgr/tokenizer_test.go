package contextmgr

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/pkoukk/tiktoken-go"
)

func TestEncodingForModel(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		// ① tiktoken 精确表
		{"gpt-4o", tiktoken.MODEL_O200K_BASE},
		{"gpt-4", tiktoken.MODEL_CL100K_BASE},
		{"gpt-3.5-turbo", tiktoken.MODEL_CL100K_BASE},
		// 前缀匹配
		{"gpt-4o-2024-08-06", tiktoken.MODEL_O200K_BASE},
		{"gpt-4-0613", tiktoken.MODEL_CL100K_BASE},
		// ② 命名推断 → cl100k（非 OpenAI 公开 tokenizer 的通用近似）
		{"claude-sonnet-4-5", tiktoken.MODEL_CL100K_BASE},
		{"deepseek-chat", tiktoken.MODEL_CL100K_BASE},
		{"kimi-k2", tiktoken.MODEL_CL100K_BASE},
		// ③ 兜底：任意未知模型 → cl100k（比启发式准）
		{"nano-gpt/custom-42", tiktoken.MODEL_CL100K_BASE},
		{"openrouter/anything", tiktoken.MODEL_CL100K_BASE},
	}
	for _, tc := range cases {
		if got := encodingForModel(tc.model); got != tc.want {
			t.Errorf("encodingForModel(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// 已知文本的 token 数断言：cl100k 下 "hello world" 恰为 2 个 token。
// 这是 tiktoken 官方文档的标准示例，可作精确回归锚点。
func TestTiktokenEstimatorCountsKnownTokens(t *testing.T) {
	est := TiktokenEstimator("claude-sonnet-4-5") // cl100k_base
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser,
			models.TextContent{Text: "hello world"},
		),
	}
	if got := est(msgs); got != 2 {
		t.Fatalf("TiktokenEstimator(hello world) = %d, want 2", got)
	}
}

// Thinking 内容也必须计入（此前 DefaultEstimator 漏掉它）。
func TestTiktokenEstimatorCountsThinking(t *testing.T) {
	est := TiktokenEstimator("deepseek-reasoner") // cl100k_base
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant,
			models.ThinkingContent{Text: "let me reason step by step"},
			models.TextContent{Text: "hello world"},
		),
	}
	total := est(msgs)
	if total <= 2 {
		t.Fatalf("estimator should count thinking + content, got %d", total)
	}
}

// cl100k 对 CJK 约 0.75 token/字（"中文测试"4 字符 = 3 token），400 个
// 中文字符的 BPE 计数应落在 [250, 450] 合理范围——与启发式（字节/4）
// 可能巧合相等（400 字 × 0.75 = 1200B/4 = 300），但 BPE 对代码/符号/
// 混合文本的适应性才是其价值所在（见 KnownTokens 精确锚点）。
func TestTiktokenEstimatorBetterThanHeuristicForCJK(t *testing.T) {
	est := TiktokenEstimator("claude-sonnet-4-5")
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser,
			models.TextContent{Text: strings.Repeat("中文测试", 100)}, // 400 字符 = 1200 字节
		),
	}
	bpe := est(msgs)
	if bpe < 250 || bpe > 450 {
		t.Fatalf("BPE count %d out of plausible range [250,450] for 400 CJK chars", bpe)
	}
	// BPE 与启发式对代码符号文本应有差异：连续标点/空白在 BPE 下按
	// 词汇表合并，字节/4 则线性放大。
	codeMsgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser,
			models.TextContent{Text: "func() { return x + y; } // comment" + strings.Repeat(" ==", 50)},
		),
	}
	codeBPE := est(codeMsgs)
	codeHeuristic := DefaultEstimator(codeMsgs)
	if codeBPE == codeHeuristic {
		t.Fatalf("BPE(%d) should differ from heuristic(%d) on code text", codeBPE, codeHeuristic)
	}
}

// 模型未知时也用 BPE（cl100k 兜底），不落回启发式。
func TestTiktokenEstimatorCoversUnknownModel(t *testing.T) {
	est := TiktokenEstimator("totally-unknown-model-xyz")
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser,
			models.TextContent{Text: "hello world"},
		),
	}
	if got := est(msgs); got != 2 {
		t.Fatalf("unknown model should still use cl100k BPE, got %d", got)
	}
}
