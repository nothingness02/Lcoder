package contextmgr

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// msgAt 构造一条指定角色、指定字符数的文本消息(4 字符 ≈ 1 token)。
func msgAt(role models.MessageRole, chars int) models.AgentMessage {
	return models.NewAgentMessage(role, models.TextContent{Text: strings.Repeat("x", chars)})
}

// toolPair 构造一对 tool_use / tool_result。DefaultEstimator 只统计顶层
// TextContent,因此 tool 消息本身贡献 0 token —— 它们用于验证切点边界
// (不得在 tool_result 之前切),而非驱动 token 预算。
func toolPair(id string, chars int) []models.AgentMessage {
	return []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant,
			models.ToolCallContent{ID: id, Name: "read", Arguments: map[string]any{}}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: id, Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: strings.Repeat("r", chars)}},
		}),
	}
}

// A: 切点由 token 预算决定,保留尾部 token ≤ 预算,且切点在 user/assistant 边界。
func TestFindCutPointTokenBudget(t *testing.T) {
	var msgs []models.AgentMessage
	for i := 0; i < 8; i++ {
		msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	}
	// 16 条 × 100 token = 1600 token;预算 500 → 保留约 5 条。
	// WithMinRecent(1) 让 token 预算成为主导约束(默认 keepRecent=10 会保 10 条)。
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(1))
	cut, split := m.findCutPoint(msgs, 500, false)
	if split {
		t.Fatal("no split expected when last turn is small")
	}
	if cut <= 0 || cut >= len(msgs) {
		t.Fatalf("cut %d out of range", cut)
	}
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatal("cut must not land before a tool_result")
	}
	if got := m.EstimateTokens(msgs[cut:]); got > 600 { // 预算 + 单条容差
		t.Fatalf("kept tail %d tokens exceeds budget 500 (with slack)", got)
	}
}

// 切点落在 tool_result 上时,向前推进到配对完整保留。
func TestFindCutPointKeepsToolPair(t *testing.T) {
	// 文本消息驱动 token 预算;中间插入一对 0-token 的 tool_use/tool_result,
	// 使预算切点恰好可能落在 tool_result 之前。
	msgs := []models.AgentMessage{msgAt(models.RoleUser, 400)}
	msgs = append(msgs, toolPair("c1", 400)...)
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, _ := m.findCutPoint(msgs, 500, false)
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatalf("cut before tool_result would orphan it")
	}
	// 保留尾部不得以孤儿 tool_result 开头。
	tail := msgs[cut:]
	if len(tail) > 0 && tail[0].Role == models.RoleToolResult {
		t.Fatal("tail starts with orphan tool_result")
	}
}

// 条数下限:短消息场景保留至少 keepRecent 条(取保留更多的切点)。
func TestFindCutPointMessageFloor(t *testing.T) {
	var msgs []models.AgentMessage
	for i := 0; i < 20; i++ {
		msgs = append(msgs, msgAt(models.RoleUser, 40), msgAt(models.RoleAssistant, 40)) // 10 token 每条
	}
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(6))
	cut, _ := m.findCutPoint(msgs, 100, false) // token 预算只够 10 条,下限要求 6 条
	if kept := len(msgs) - cut; kept < 6 {
		t.Fatalf("message floor violated: kept %d < 6", kept)
	}
}

// 最后 user 保护:非 reactive 不切进最后一轮。
func TestFindCutPointProtectsLastTurn(t *testing.T) {
	var msgs []models.AgentMessage
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	msgs = append(msgs, msgAt(models.RoleUser, 400)) // 最后一轮开始 idx=2
	// 最后一轮自身用大文本撑大,使预算切点想切进最后一轮。
	msgs = append(msgs, msgAt(models.RoleAssistant, 40000))
	// WithMinRecent(1) 避免条数下限(默认 10 > n=4)把切点压回 0。
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000}, WithMinRecent(1))
	cut, split := m.findCutPoint(msgs, 500, false) // allowSplit=false
	if split {
		t.Fatal("split must not happen without allowSplit")
	}
	if cut != 2 {
		t.Fatalf("expected cut at last turn start (2), got %d", cut)
	}
}

// B: reactive 允许 split turn,切在最后一轮内部的合法边界。
func TestFindCutPointSplitTurn(t *testing.T) {
	var msgs []models.AgentMessage
	msgs = append(msgs, msgAt(models.RoleUser, 400), msgAt(models.RoleAssistant, 400))
	msgs = append(msgs, msgAt(models.RoleUser, 400)) // 最后一轮开始 idx=2
	// 最后一轮内部:大文本 assistant + 一对 tool_use/tool_result + 结尾 assistant。
	msgs = append(msgs, msgAt(models.RoleAssistant, 40000))
	msgs = append(msgs, toolPair("c1", 400)...)
	msgs = append(msgs, msgAt(models.RoleAssistant, 400), msgAt(models.RoleAssistant, 400))
	m := NewManager(TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 1000})
	cut, split := m.findCutPoint(msgs, 500, true) // allowSplit=true
	if !split {
		t.Fatal("expected split turn")
	}
	if cut <= 2 {
		t.Fatalf("split cut must be inside the last turn, got %d", cut)
	}
	if msgs[cut].Role == models.RoleToolResult {
		t.Fatal("split cut must not orphan a tool_result")
	}
}
