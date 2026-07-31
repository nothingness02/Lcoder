package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func activity(callID string, kind events.SubagentActivityKind, text string) events.SubagentActivityEvent {
	return events.SubagentActivityEvent{
		Base:             events.Base{Type: events.SubagentActivity},
		AgentID:          "agent-x",
		ParentToolCallID: callID,
		Profile:          "explore",
		Kind:             kind,
		Text:             text,
	}
}

func newActivityModel(t *testing.T) *Model {
	t.Helper()
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
	return m
}

// Mirrored activity lands in the parent tool call's block as nested lines:
// started marks live, text accumulates into the tail, tool lines append in
// order, completed flushes the tail and clears live.
func TestSubagentActivityRouting(t *testing.T) {
	m := newActivityModel(t)
	m.appendBlock(block{kind: components.BlockTool, id: "call-1", toolName: "subagent", toolRunning: true})

	m.handleEvent(activity("call-1", events.SubagentStarted, ""))
	m.handleEvent(activity("call-1", events.SubagentText, "reading "))
	m.handleEvent(activity("call-1", events.SubagentText, "main.go"))
	m.handleEvent(activity("call-1", events.SubagentToolStart, "grep"))
	m.handleEvent(activity("call-1", events.SubagentToolEnd, "grep"))
	m.handleEvent(activity("call-1", events.SubagentCompleted, ""))

	b := m.blocks[0]
	if b.subagentLive {
		t.Fatal("completed should clear live")
	}
	want := []string{"reading main.go", "→ grep", "✓ grep"}
	if len(b.subagentLines) != len(want) {
		t.Fatalf("lines = %v, want %v", b.subagentLines, want)
	}
	for i, ln := range want {
		if b.subagentLines[i] != ln {
			t.Fatalf("line %d = %q, want %q", i, b.subagentLines[i], ln)
		}
	}
	if b.subagentTail != "" {
		t.Fatalf("tail should be flushed, got %q", b.subagentTail)
	}
}

// Activity for an unknown parent tool call is dropped silently.
func TestSubagentActivityUnknownCallDropped(t *testing.T) {
	m := newActivityModel(t)
	m.handleEvent(activity("no-such-call", events.SubagentText, "x"))
	if len(m.blocks) != 0 {
		t.Fatalf("no block should be created for unknown parent call, got %+v", m.blocks)
	}
}

// The nested activity section renders under the tool call, and the compact
// view caps long activity logs.
func TestToolResultComponentRendersSubagentActivity(t *testing.T) {
	comp := components.NewToolResultComponent("call-1", "subagent", "", "done", false, false, time.Now(), 0)
	lines := []string{"first line", "→ grep", "✓ grep"}
	comp.SetSubagentActivity(lines, "stream tail", true)

	out := comp.Render(80, false)
	for _, want := range []string{"subagent (running)", "first line", "→ grep", "stream tail"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render should contain %q:\n%s", want, out)
		}
	}

	many := make([]string, 20)
	for i := range many {
		many[i] = strings.Repeat("x", 3)
	}
	comp.SetSubagentActivity(many, "", false)
	compact := comp.Render(80, false)
	if strings.Count(compact, "  │ ") > 8+1 { // 8 activity lines + preview allowance
		t.Fatalf("compact view should cap activity lines, got:\n%s", compact)
	}
	expanded := comp.Render(80, true)
	if strings.Count(expanded, "xxx") != 20 {
		t.Fatalf("expanded view should show all activity lines, got %d", strings.Count(expanded, "xxx"))
	}
}

func activityWithAgent(agentID, callID string, kind events.SubagentActivityKind, text string) events.SubagentActivityEvent {
	return events.SubagentActivityEvent{
		Base:             events.Base{Type: events.SubagentActivity},
		AgentID:          agentID,
		ParentToolCallID: callID,
		Profile:          agentID,
		Kind:             kind,
		Text:             text,
	}
}

// Per-child state: children register on first activity, tool starts count,
// completion settles each child with its final status, and the block only
// goes not-live once every child has settled.
func TestSubagentGroupState(t *testing.T) {
	m := newActivityModel(t)
	m.appendBlock(block{kind: components.BlockTool, id: "call-1", toolName: "subagent", toolRunning: true})

	m.handleEvent(activityWithAgent("explore", "call-1", events.SubagentStarted, ""))
	m.handleEvent(activityWithAgent("coder", "call-1", events.SubagentStarted, ""))
	m.handleEvent(activityWithAgent("explore", "call-1", events.SubagentToolStart, "grep"))
	m.handleEvent(activityWithAgent("explore", "call-1", events.SubagentCompleted, "completed"))

	b := m.blocks[0]
	if len(b.subagentChildren) != 2 {
		t.Fatalf("expected 2 children, got %d", len(b.subagentChildren))
	}
	explore := b.subagentChildren["explore"]
	if explore.tools != 1 || explore.status != "completed" {
		t.Fatalf("explore child state wrong: %+v", explore)
	}
	if b.subagentLive == false {
		t.Fatal("block should stay live while coder still runs")
	}

	m.handleEvent(activityWithAgent("coder", "call-1", events.SubagentCompleted, "timeout"))
	// Re-read the block: it is a value copy and went stale after more events.
	b = m.blocks[0]
	if b.subagentChildren["coder"].status != "timeout" {
		t.Fatalf("coder status = %q, want timeout", b.subagentChildren["coder"].status)
	}
	if b.subagentLive {
		t.Fatal("block should go not-live once all children settled")
	}

	rows := subagentChildRows(b)
	if len(rows) != 2 || rows[0].Profile != "explore" || rows[1].Profile != "coder" {
		t.Fatalf("rows should preserve spawn order, got %+v", rows)
	}
}

// Group rendering: 2+ children get the aggregate header and tree rows; a
// single child keeps the plain activity section without a group header.
func TestSubagentGroupRendering(t *testing.T) {
	comp := components.NewToolResultComponent("c1", "subagent", "", "", false, true, time.Now(), 0)
	comp.SetSubagentChildren([]components.SubagentChildRow{
		{Profile: "explore", Status: "completed", Tools: 4, Elapsed: 8000000000},
		{Profile: "coder", Status: "running", Tools: 2, Started: time.Now()},
	})
	comp.SetSubagentActivity([]string{"some work"}, "", true)

	out := comp.Render(80, false)
	for _, want := range []string{"2 subagents (1 done, 1 running)", "├─ explore", "└─ coder", "· 4 tools", "completed", "running"} {
		if !strings.Contains(out, want) {
			t.Fatalf("group render missing %q:\n%s", want, out)
		}
	}

	solo := components.NewToolResultComponent("c2", "subagent", "", "", false, true, time.Now(), 0)
	solo.SetSubagentChildren([]components.SubagentChildRow{{Profile: "explore", Status: "running"}})
	solo.SetSubagentActivity([]string{"work"}, "", true)
	soloOut := solo.Render(80, false)
	if strings.Contains(soloOut, "subagents (") {
		t.Fatalf("single child should not get a group header:\n%s", soloOut)
	}
}

// A background completion notice renders as a system line immediately.
func TestBackgroundNoticeRendersSystemLine(t *testing.T) {
	m := newActivityModel(t)
	m.handleEvent(events.BackgroundNoticeEvent{
		Base: events.Base{Type: events.BackgroundNotice},
		Text: "background subagent bg-1 (explore) completed",
	})
	if len(m.blocks) != 1 || m.blocks[0].kind != components.BlockSystem {
		t.Fatalf("notice should become a system block, got %+v", m.blocks)
	}
}
