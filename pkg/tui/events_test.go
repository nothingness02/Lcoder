package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestErrorEventShowsInFixedRegionNotTranscript(t *testing.T) {
	m, _, _ := newTestModel()
	m.mainWidth = 80
	before := len(m.blocks)
	m.handleEvent(events.ErrorEvent{Base: events.Base{Type: events.Error, Turn: 1}, Message: "boom"})
	if len(m.blocks) != before {
		t.Fatalf("error must not append a transcript block: %d -> %d", before, len(m.blocks))
	}
	if m.errMsg != "boom" {
		t.Fatalf("errMsg = %q, want boom", m.errMsg)
	}
	if !strings.Contains(stripANSI(m.bottomRegion()), "boom") {
		t.Fatal("error should render in the fixed bottom region")
	}
}

func TestAgentDoneErrorGoesToFixedRegion(t *testing.T) {
	m, _, _ := newTestModel()
	m.mainWidth = 80
	before := len(m.blocks)
	m.onAgentDone(errors.New("run failed"))
	if len(m.blocks) != before {
		t.Fatalf("run error must not append a transcript block: %d -> %d", before, len(m.blocks))
	}
	if m.errMsg != "run failed" {
		t.Fatalf("errMsg = %q, want run failed", m.errMsg)
	}
}

func TestCompactionIndicatorLifecycle(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.CompactionStartedEvent{})
	if !m.compacting {
		t.Fatal("CompactionStartedEvent must set compacting")
	}

	m.state = stateProcessing
	m.mainWidth = 80
	if view := m.statusLineView(); !strings.Contains(view, "压缩中") {
		t.Fatalf("status line must show compacting indicator, got %q", view)
	}

	m.handleEvent(events.CompactionCommittedEvent{})
	if m.compacting {
		t.Fatal("CompactionCommittedEvent must clear compacting")
	}
	if view := m.statusLineView(); strings.Contains(view, "压缩中") {
		t.Fatalf("indicator must be gone after commit, got %q", view)
	}
}

func TestTurnEndEventAddsToolSummaryBelowToolCalls(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("file1\nfile2"),
		IsError:    false,
	})

	toolCount := len(m.blocks)
	if toolCount == 0 {
		t.Fatal("expected a tool block after tool events")
	}

	m.handleEvent(events.TurnEndEvent{
		Message:     models.AgentMessage{Role: models.RoleAssistant},
		ToolResults: []models.AgentMessage{models.NewAgentMessage(models.RoleToolResult, models.TextContent{Text: "file1\nfile2"})},
	})

	if len(m.blocks) != toolCount+1 {
		t.Fatalf("expected summary block appended right after tools, got %d blocks (want %d)", len(m.blocks), toolCount+1)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != components.BlockSystem {
		t.Fatalf("expected system summary block, got %v", last.kind)
	}
	if !strings.Contains(last.raw, "1 tool") {
		t.Fatalf("expected summary text to mention tool count, got %q", last.raw)
	}
	if len(m.turnTools) != 0 {
		t.Fatalf("turnTools should be reset after summary, got %d", len(m.turnTools))
	}
}

func TestAgentDoneDoesNotDuplicateToolSummary(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("file1"),
		IsError:    false,
	})
	m.handleEvent(events.TurnEndEvent{
		Message:     models.AgentMessage{Role: models.RoleAssistant},
		ToolResults: []models.AgentMessage{models.NewAgentMessage(models.RoleToolResult, models.TextContent{Text: "file1"})},
	})

	afterTurnEnd := len(m.blocks)
	m.onAgentDone(nil)
	if len(m.blocks) != afterTurnEnd {
		t.Fatalf("onAgentDone should not append another summary, blocks changed from %d to %d", afterTurnEnd, len(m.blocks))
	}
}

func TestMessageEndUsesStreamingBlockID(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing

	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "stream-id"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: "hello"})

	// Provider may finalize with a different message object/ID.
	final := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hello world"})
	final.ID = "final-id"
	m.handleEvent(events.MessageEndEvent{Message: final})

	var assistantBlocks int
	var lastRaw string
	for _, b := range m.blocks {
		if b.kind == components.BlockAssistant {
			assistantBlocks++
			lastRaw = b.raw
		}
	}
	if assistantBlocks != 1 {
		t.Fatalf("expected 1 assistant block, got %d", assistantBlocks)
	}
	if lastRaw != "hello world" {
		t.Fatalf("expected 'hello world', got %q", lastRaw)
	}
}

func TestThinkingDeltaDoesNotLeakIntoContent(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing

	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "a1"}})
	m.handleEvent(events.MessageUpdateEvent{Delta: "reasoning ", IsThinking: true})
	m.handleEvent(events.MessageUpdateEvent{Delta: "step", IsThinking: true})
	m.handleEvent(events.MessageUpdateEvent{Delta: "answer"})

	final := models.NewAgentMessage(models.RoleAssistant,
		models.ThinkingContent{Text: "reasoning step"},
		models.TextContent{Text: "answer"},
	)
	final.ID = "a1"
	m.handleEvent(events.MessageEndEvent{Message: final})

	for _, b := range m.blocks {
		if b.kind != components.BlockAssistant {
			continue
		}
		if b.raw != "answer" {
			t.Fatalf("content should be answer only, got %q", b.raw)
		}
		if b.thinking != "reasoning step" {
			t.Fatalf("thinking should be reasoning step, got %q", b.thinking)
		}
		ac, ok := m.components[0].(*components.AssistantComponent)
		if !ok {
			t.Fatalf("expected assistant component, got %T", m.components[0])
		}
		rendered := ac.Render(40, false)
		// Collapsed thinking surfaces the first non-empty line as a one-line
		// summary ("Thinking: <first line>" while streaming, "Thought: <first
		// line> · Ns" once the duration is recorded — the exact label is
		// covered by thinking_duration_test.go). The reasoning trace must stay
		// out of the content area, which the b.raw assertion above already
		// pins to "answer".
		if !strings.Contains(rendered, "reasoning step") {
			t.Fatalf("collapsed thinking should show first-line summary, got %q", rendered)
		}
	}
}

func TestToolSummaryAppearsBeforeNextAssistantMessage(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "c1",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("file1"),
		IsError:    false,
	})
	m.handleEvent(events.TurnEndEvent{
		Message:     models.AgentMessage{Role: models.RoleAssistant},
		ToolResults: []models.AgentMessage{models.NewAgentMessage(models.RoleToolResult, models.TextContent{Text: "file1"})},
	})
	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "a2"}})

	if len(m.blocks) < 3 {
		t.Fatalf("expected tool + summary + assistant, got %d blocks", len(m.blocks))
	}
	summaryIdx := len(m.blocks) - 2
	assistantIdx := len(m.blocks) - 1
	if m.blocks[summaryIdx].kind != components.BlockSystem {
		t.Fatalf("expected summary block before final assistant, got %v", m.blocks[summaryIdx].kind)
	}
	if m.blocks[assistantIdx].kind != components.BlockAssistant {
		t.Fatalf("expected assistant block after summary, got %v", m.blocks[assistantIdx].kind)
	}
}

func TestBlocksFromMessagesMergesToolResults(t *testing.T) {
	toolCallID := "call-1"
	prior := []models.AgentMessage{
		models.UserMessage("q"),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "let me check"},
			models.ToolCallContent{ID: toolCallID, Name: "bash", Arguments: map[string]any{"command": "ls"}},
		),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: toolCallID,
			Name:       "bash",
			Content:    []models.ContentPart{models.TextContent{Text: "file1"}},
			IsError:    false,
		}),
	}

	blocks := blocksFromMessages(prior, nil)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (user, assistant, merged tool), got %d", len(blocks))
	}
	if blocks[0].kind != components.BlockUser || blocks[0].raw != "q" {
		t.Fatalf("first block should be user question, got %+v", blocks[0])
	}
	if blocks[1].kind != components.BlockAssistant || blocks[1].raw != "let me check" {
		t.Fatalf("second block should be assistant reply, got %+v", blocks[1])
	}
	tool := blocks[2]
	if tool.kind != components.BlockTool {
		t.Fatalf("third block should be tool, got %v", tool.kind)
	}
	if tool.toolName != "bash" {
		t.Fatalf("tool name = %q, want bash", tool.toolName)
	}
	if tool.toolResult != "file1" {
		t.Fatalf("tool result = %q, want file1", tool.toolResult)
	}
	if tool.toolErr {
		t.Fatal("tool error should be false")
	}
}

// The idle status line surfaces context budget usage once the agent reports a
// drop limit. ContextStats() is expensive, so it is sampled only at the AgentEnd
// boundary and cached on the model.
func TestContextPctShownAfterAgentEnd(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.ContextStatsVal = agentapi.ContextStats{Total: 40000, DropLimit: 100000}
	m.mainWidth = 80

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != 40 {
		t.Fatalf("contextPct = %d, want 40", m.contextPct)
	}
	view := stripANSI(m.statusLineView())
	if !strings.Contains(view, "context: 40% (39.1k/97.7k)") {
		t.Fatalf("status line should show kimi-style context usage, got %q", view)
	}
}

// A nil/absent drop limit (tests, unconfigured budget) must hide the segment
// rather than divide by zero or render a bogus 0%.
func TestContextPctHiddenWithoutDropLimit(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.ContextStatsVal = agentapi.ContextStats{} // FakeAgent default: no stats
	m.mainWidth = 80

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != -1 {
		t.Fatalf("contextPct = %d, want -1 (hidden)", m.contextPct)
	}
	if view := stripANSI(m.statusLineView()); strings.Contains(view, "ctx") {
		t.Fatalf("status line must hide ctx%% without a drop limit, got %q", view)
	}
}

// A drop limit of zero is as unusable as a missing one; guard the division.
func TestContextPctHiddenOnZeroDropLimit(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.ContextStatsVal = agentapi.ContextStats{Total: 5000}

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != -1 {
		t.Fatalf("contextPct = %d, want -1 on zero drop limit", m.contextPct)
	}
}

// The status line prefers the provider-reported real prompt total over the
// char-based heuristic estimate: the real number is what actually counts
// against the context window on the wire.
func TestContextPctPrefersRealPromptTotal(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.ContextStatsVal = agentapi.ContextStats{
		Total:           40000, // heuristic (4 chars/token)
		RealPromptTotal: 90000, // provider-reported: input + cache_read + cache_creation
		DropLimit:       100000,
	}
	m.mainWidth = 80

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != 90 {
		t.Fatalf("contextPct = %d, want 90 from real prompt total", m.contextPct)
	}
	if m.contextUsedTok != 90000 {
		t.Fatalf("contextUsedTok = %d, want 90000 (real)", m.contextUsedTok)
	}
	view := stripANSI(m.statusLineView())
	if !strings.Contains(view, "context: 90% (87.9k/97.7k)") {
		t.Fatalf("status line should show real-based usage, got %q", view)
	}
}

// Without a real prompt total the status line falls back to the heuristic
// estimate, so a fresh session (or a provider that reports no usage) still
// shows a sane percentage.
func TestContextPctFallsBackToHeuristicWithoutRealUsage(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.ContextStatsVal = agentapi.ContextStats{Total: 40000, DropLimit: 100000} // no real prompt total
	m.mainWidth = 80

	m.handleEvent(events.AgentEndEvent{})

	if m.contextPct != 40 {
		t.Fatalf("contextPct = %d, want 40 from heuristic fallback", m.contextPct)
	}
	if m.contextUsedTok != 40000 {
		t.Fatalf("contextUsedTok = %d, want 40000 (heuristic)", m.contextUsedTok)
	}
}

func TestFinishToolPassesDetailsIntoBlock(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.ToolExecutionStartEvent{
		ToolCallID: "w1",
		ToolName:   "write",
		Args:       map[string]any{"path": "a.go", "content": "new\ncontent"},
	})
	m.handleEvent(events.ToolExecutionEndEvent{
		ToolCallID: "w1",
		ToolName:   "write",
		Result: models.ToolExecutionResult{
			Content: []models.ContentPart{models.TextContent{Text: "Wrote 11 characters to a.go"}},
			Details: map[string]any{"path": "a.go", "old_content": "old\ncontent"},
		},
	})

	var b *block
	for i := range m.blocks {
		if m.blocks[i].kind == components.BlockTool && m.blocks[i].id == "w1" {
			b = &m.blocks[i]
		}
	}
	if b == nil {
		t.Fatal("expected a write tool block")
	}
	if got, ok := b.toolDetails["old_content"].(string); !ok || got != "old\ncontent" {
		t.Fatalf("details not passed into block, got %v", b.toolDetails)
	}
	// The component built from the block renders the overwrite as a diff.
	comp := toComponent(*b).(*components.ToolResultComponent)
	out := comp.Render(80, false)
	if !strings.Contains(out, "- old") || !strings.Contains(out, "+ new") {
		t.Fatalf("expected overwrite diff in component, got:\n%s", out)
	}
}

func TestBlocksFromMessagesWithoutDetailsDegrades(t *testing.T) {
	msgs := []models.AgentMessage{
		{
			Role: models.RoleAssistant,
			ID:   "a1",
			Content: []models.ContentPart{
				models.ToolCallContent{
					ID:   "c1",
					Name: "write",
					Arguments: map[string]any{
						"path":    "a.go",
						"content": "line1\nline2",
					},
				},
			},
		},
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1",
			Name:       "write",
			Content:    []models.ContentPart{models.TextContent{Text: "Wrote 11 characters to a.go"}},
		}),
	}
	blocks := blocksFromMessages(msgs, nil)
	var tb *block
	for i := range blocks {
		if blocks[i].kind == components.BlockTool {
			tb = &blocks[i]
		}
	}
	if tb == nil {
		t.Fatal("expected a tool block")
	}
	if tb.toolDetails != nil {
		t.Fatalf("restored blocks must not have details, got %v", tb.toolDetails)
	}
	// Without details the restored write renders the content preview, no diff.
	out := toComponent(*tb).Render(80, false)
	if !strings.Contains(out, "line1") {
		t.Fatalf("restored write should fall back to content preview, got:\n%s", out)
	}
}
