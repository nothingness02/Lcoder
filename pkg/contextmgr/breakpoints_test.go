package contextmgr

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestBuildTurnRequestCacheBreakpoints(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 2000, TargetTotal: 1800, ReserveOutput: 200})
	m.SetSystemPrompt(strings.Repeat("a", 1100))
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	m.AppendRecent(models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hi"}))

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.CacheBreakpoints) == 0 {
		t.Fatalf("expected breakpoints, got %v", req.CacheBreakpoints)
	}
	foundFirst := false
	foundLastUser := false
	for _, bp := range req.CacheBreakpoints {
		if bp == 0 {
			foundFirst = true
		}
		if bp == 0 {
			foundLastUser = true
		}
	}
	if !foundFirst {
		t.Fatalf("expected breakpoint at first message, got %v", req.CacheBreakpoints)
	}
	if !foundLastUser {
		t.Fatalf("expected breakpoint at last user message, got %v", req.CacheBreakpoints)
	}
}

func TestExplicitCacheHintBreakpoint(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 2000, TargetTotal: 1800, ReserveOutput: 200})
	b := NewBlock(BlockRecent, "recent", StabilityDynamic, 100,
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))
	b.CacheHint = CacheHintBreakpoint
	m.SetBlock(b)

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, bp := range req.CacheBreakpoints {
		if bp == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected explicit breakpoint, got %v", req.CacheBreakpoints)
	}
}

func TestCachePolicyNone_NoBreakpoints(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0}, WithCacheHintPolicy(CachePolicyNone))
	mgr.SetSystemPrompt(strings.Repeat("sys ", 300)) // > 256 tokens
	mgr.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	})
	req, err := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	if len(req.CacheBreakpoints) != 0 {
		t.Fatalf("expected no breakpoints with none policy, got %v", req.CacheBreakpoints)
	}
}

func TestCachePolicyAggressive_PrefixAlwaysAnchored(t *testing.T) {
	small := "sys " // < 256 tokens
	mgrDef := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0})
	mgrDef.SetSystemPrompt(small)
	mgrDef.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hello"}),
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	})
	reqDef, _ := mgrDef.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if containsBreakpoint(reqDef.CacheBreakpoints, 0) {
		t.Fatalf("default policy should not breakpoint tiny prefix at 0, got %v", reqDef.CacheBreakpoints)
	}

	mgrAgg := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0}, WithCacheHintPolicy(CachePolicyAggressive))
	mgrAgg.SetSystemPrompt(small)
	mgrAgg.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hello"}),
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	})
	reqAgg, _ := mgrAgg.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if !containsBreakpoint(reqAgg.CacheBreakpoints, 0) {
		t.Fatalf("aggressive policy should breakpoint any non-empty prefix at 0, got %v", reqAgg.CacheBreakpoints)
	}
}

func TestCacheHintSkip_NoBreakpointOnBlock(t *testing.T) {
	mgr := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0})
	mgr.SetSystemPrompt(strings.Repeat("sys ", 300))
	retrieval := NewBlock(BlockRetrieval, "retrieval", StabilityStable, 50,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "rag result"}))
	retrieval.CacheHint = CacheHintSkip
	mgr.SetBlock(retrieval)
	mgr.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	})
	req, _ := mgr.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	for _, bp := range req.CacheBreakpoints {
		// retrieval block messages start at index 0 because it is not a system block,
		// but with CacheHintSkip it must not be anchored.
		if bp == 0 {
			t.Fatalf("CacheHintSkip block got breakpoint at 0: %v", req.CacheBreakpoints)
		}
	}
}

// TestTailBreakpointFollowsToolResults pins the cache anchor to the real tail of
// the conversation. Inside one agent turn the model emits tool calls and the
// harness appends tool results, so the last message is usually a tool_result,
// not a user message. Anchoring on the last RoleUser message would leave every
// tool_use/tool_result pair accumulated during the turn outside the cached
// prefix, re-billing the whole growing tail as fresh input on each step.
func TestTailBreakpointFollowsToolResults(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0})
	m.SetSystemPrompt(strings.Repeat("sys ", 300))
	m.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "fix the bug"}),
		models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{ID: "t1", Name: "read_file"}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "t1",
			Content:    []models.ContentPart{models.TextContent{Text: "file body"}},
		}),
	})

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	if !containsBreakpoint(req.CacheBreakpoints, 2) {
		t.Fatalf("expected tail breakpoint at the tool_result (index 2), got %v", req.CacheBreakpoints)
	}
}

// TestTailBreakpointSkipsEphemeral keeps the anchor on stable history: ephemeral
// reminders are re-injected with different content every turn, so anchoring on
// them would bust the cached prefix on every request.
func TestTailBreakpointSkipsEphemeral(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0})
	m.SetSystemPrompt(strings.Repeat("sys ", 300))
	m.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hello"}),
	})
	m.AddEphemeralReminder("do not forget the todo list")

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	// The ephemeral message is appended at index 2; the anchor must stay at 1.
	if containsBreakpoint(req.CacheBreakpoints, 2) {
		t.Fatalf("ephemeral reminder must not anchor the cache, got %v", req.CacheBreakpoints)
	}
	if !containsBreakpoint(req.CacheBreakpoints, 1) {
		t.Fatalf("expected tail breakpoint at last stable message (index 1), got %v", req.CacheBreakpoints)
	}
}

// TestBreakpointsAreSortedAndDeduped guards the wire contract: Anthropic caps a
// request at 4 cache_control blocks, so duplicate or unordered indices waste the
// budget and can push a valid request over the limit.
func TestBreakpointsAreSortedAndDeduped(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 10000, ReserveOutput: 0})
	m.SetSystemPrompt(strings.Repeat("sys ", 300))
	// A single-message recent block: the prefix anchor and the tail anchor both
	// resolve to index 0, which previously emitted [0 0].
	m.ReplaceRecent([]models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	})

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	seen := make(map[int]bool, len(req.CacheBreakpoints))
	prev := -1
	for _, bp := range req.CacheBreakpoints {
		if seen[bp] {
			t.Fatalf("duplicate breakpoint %d in %v", bp, req.CacheBreakpoints)
		}
		if bp < prev {
			t.Fatalf("breakpoints not ascending: %v", req.CacheBreakpoints)
		}
		seen[bp] = true
		prev = bp
	}
}

func containsBreakpoint(bps []int, idx int) bool {
	for _, b := range bps {
		if b == idx {
			return true
		}
	}
	return false
}

// CachePolicyNone must switch the request into cache="none" as well as clearing
// the breakpoints: the Anthropic adapter marks the system block, the last tool
// definition, and a fallback tail message on its own, so an empty breakpoint
// list alone leaves caching on.
func TestCachePolicyNoneDisablesCacheEntirely(t *testing.T) {
	m := NewManager(
		TokenBudget{MaxTotal: 2000, TargetTotal: 1800, ReserveOutput: 200},
		WithCacheHintPolicy(CachePolicyNone),
	)
	m.SetSystemPrompt(strings.Repeat("a", 1100))
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Cache != "none" {
		t.Fatalf("expected cache %q, got %q", "none", req.Cache)
	}
	if len(req.CacheBreakpoints) != 0 {
		t.Fatalf("expected no breakpoints, got %v", req.CacheBreakpoints)
	}
}

func TestDefaultPolicyKeepsCacheAuto(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 2000, TargetTotal: 1800, ReserveOutput: 200})
	m.SetSystemPrompt(strings.Repeat("a", 1100))
	m.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hello"}))

	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "anthropic", ID: "claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Cache != "auto" {
		t.Fatalf("expected cache %q, got %q", "auto", req.Cache)
	}
}
