package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/testutil"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func assistantMsgWithID(id, text string) models.AgentMessage {
	msg := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: text})
	msg.ID = id
	return msg
}

func turnUsageEvent(msgID string, prompt, completion int, cost float64) events.TurnEndEvent {
	return events.TurnEndEvent{
		Message: assistantMsgWithID(msgID, "done"),
		Usage: models.LLMUsage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      prompt + completion,
			TotalCost:        cost,
		},
	}
}

// The live path: usage arrives at TurnEnd (after MessageEnd committed the
// block) and is patched onto the block; the status-line total accumulates.
func TestTurnEndAttachesUsageToCommittedBlock(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.MessageStartEvent{Message: assistantMsgWithID("a1", "")})
	m.handleEvent(events.MessageUpdateEvent{Delta: "hello"})
	m.handleEvent(events.MessageEndEvent{Message: assistantMsgWithID("a1", "hello")})
	m.handleEvent(turnUsageEvent("a1", 10, 5, 0.0123))

	if len(m.blocks) != 1 || m.blocks[0].kind != components.BlockAssistant {
		t.Fatalf("blocks = %+v, want one assistant block", m.blocks)
	}
	u := m.blocks[0].usage
	if u == nil {
		t.Fatal("assistant block has no usage after TurnEnd")
	}
	if u.inputTokens != 10 || u.outputTokens != 5 || u.totalTokens != 15 {
		t.Fatalf("block usage = %+v, want 10/5/15", u)
	}
	if m.totalCost != 0.0123 {
		t.Fatalf("totalCost = %v, want 0.0123", m.totalCost)
	}
}

// A provider may finalize the message under a different id than the streamed
// partial; TurnEnd must still find the committed block via the id recorded at
// MessageEnd.
func TestTurnEndUsageMatchesRenamedFinalMessage(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.MessageStartEvent{Message: assistantMsgWithID("partial-1", "")})
	m.handleEvent(events.MessageEndEvent{Message: assistantMsgWithID("final-1", "hello")})
	m.handleEvent(turnUsageEvent("final-1", 8, 4, 0.01))

	if len(m.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(m.blocks))
	}
	if m.blocks[0].id != "partial-1" {
		t.Fatalf("block id = %q, want the streaming id partial-1", m.blocks[0].id)
	}
	if m.blocks[0].usage == nil || m.blocks[0].usage.totalTokens != 12 {
		t.Fatalf("block usage = %+v, want 12 total tokens", m.blocks[0].usage)
	}
	if m.totalCost != 0.01 {
		t.Fatalf("totalCost = %v, want 0.01", m.totalCost)
	}
}

// A turn whose provider reported no usage must not render a "0 tokens" footer
// or move the total.
func TestTurnEndWithoutUsageLeavesBlockAlone(t *testing.T) {
	m, _, _ := newTestModel()

	m.handleEvent(events.MessageStartEvent{Message: assistantMsgWithID("a1", "")})
	m.handleEvent(events.MessageEndEvent{Message: assistantMsgWithID("a1", "hello")})
	m.handleEvent(events.TurnEndEvent{Message: assistantMsgWithID("a1", "hello")})

	if m.blocks[0].usage != nil {
		t.Fatalf("block usage = %+v, want nil for a usage-less turn", m.blocks[0].usage)
	}
	if m.totalCost != 0 {
		t.Fatalf("totalCost = %v, want 0", m.totalCost)
	}
}

// The status-line total is seeded from the host's UsageSummary at startup,
// then accumulates per TurnEnd.
func TestTotalCostSeededFromUsageSummary(t *testing.T) {
	ag := &testutil.FakeAgent{
		SessionIDVal:    "abc123",
		UsageSummaryVal: agentapi.UsageSummary{Turns: 3, TotalCost: 0.5},
	}
	m := NewModel(ag, host.Services{Bus: events.New()}, DisplayConfig{
		CWD:        ".",
		ModelRef:   "openai/gpt-4o-mini",
		ThemeStyle: "dark",
	})
	defer m.Close()

	if m.totalCost != 0.5 {
		t.Fatalf("totalCost = %v, want the seeded 0.5", m.totalCost)
	}
	m.handleEvent(events.MessageStartEvent{Message: assistantMsgWithID("a1", "")})
	m.handleEvent(events.MessageEndEvent{Message: assistantMsgWithID("a1", "hi")})
	m.handleEvent(turnUsageEvent("a1", 10, 5, 0.25))
	if m.totalCost != 0.75 {
		t.Fatalf("totalCost = %v, want 0.75 (seed + turn increment)", m.totalCost)
	}
}

// Switching sessions re-reads the aggregate and the ledger from the core:
// the total follows the new session, and history blocks get their usage.
func TestReloadFromCoreRefreshesUsageState(t *testing.T) {
	m, ag, _ := newTestModel()
	defer m.Close()

	m.totalCost = 9.99 // stale value from the "previous" session
	ag.UsageSummaryVal = agentapi.UsageSummary{Turns: 1, TotalCost: 0.42}
	ag.Messages = []models.AgentMessage{assistantMsgWithID("m1", "old reply")}
	ag.UsageLedgerVal = map[string]models.LLMUsage{
		"m1": {PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10, TotalCost: 0.42},
	}

	m.reloadFromCore()

	if m.totalCost != 0.42 {
		t.Fatalf("totalCost = %v, want the re-read 0.42", m.totalCost)
	}
	if len(m.blocks) != 1 || m.blocks[0].usage == nil || m.blocks[0].usage.totalTokens != 10 {
		t.Fatalf("rebuilt blocks = %+v, want the ledger usage on m1", m.blocks)
	}
}

// Picking a session in the picker switches the displayed cost total and the
// history usage footers to the opened session's ledger.
func TestOpenSessionSwitchesUsageLedger(t *testing.T) {
	ag := &testutil.FakeAgent{
		SessionIDVal:    "s1",
		UsageSummaryVal: agentapi.UsageSummary{Turns: 5, TotalCost: 1.5},
		SessionMsgs:     map[string][]models.AgentMessage{"s2": {assistantMsgWithID("x1", "other reply")}},
		SessionUsage:    map[string]agentapi.UsageSummary{"s2": {Turns: 1, TotalCost: 0.07}},
		SessionLedger: map[string]map[string]models.LLMUsage{
			"s2": {"x1": {PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5, TotalCost: 0.07}},
		},
	}
	m := newTestCoreModel(ag)
	defer m.Close()
	if m.totalCost != 1.5 {
		t.Fatalf("totalCost before switch = %v, want 1.5", m.totalCost)
	}

	m.openSessionByID("s2")

	if m.totalCost != 0.07 {
		t.Fatalf("totalCost after switch = %v, want the opened session's 0.07", m.totalCost)
	}
	if len(m.blocks) != 1 || m.blocks[0].usage == nil || m.blocks[0].usage.totalTokens != 5 {
		t.Fatalf("blocks after switch = %+v, want x1 with its ledger usage", m.blocks)
	}
}

// History rebuild: assistant blocks get usage from the ledger by message id;
// messages without an entry render no footer.
func TestBlocksFromMessagesAttachesLedgerUsage(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("q"),
		assistantMsgWithID("m1", "first"),
		assistantMsgWithID("m2", "second"),
	}
	ledger := map[string]models.LLMUsage{
		"m1": {PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10, TotalCost: 0.01},
	}

	blocks := blocksFromMessages(msgs, ledger)

	var a1, a2 *block
	for i := range blocks {
		switch blocks[i].id {
		case "m1":
			a1 = &blocks[i]
		case "m2":
			a2 = &blocks[i]
		}
	}
	if a1 == nil || a2 == nil {
		t.Fatalf("blocks = %+v, want both assistant blocks", blocks)
	}
	if a1.usage == nil || a1.usage.totalTokens != 10 || a1.usage.cost != 0.01 {
		t.Fatalf("m1 usage = %+v, want 10 tokens/0.01", a1.usage)
	}
	if a2.usage != nil {
		t.Fatalf("m2 usage = %+v, want nil (no ledger entry)", a2.usage)
	}
}
