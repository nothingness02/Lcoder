package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

// oldAssistantMsg builds an assistant message with a timestamp far enough in
// the past to count as cache-cold (default threshold is 1h).
func oldAssistantMsg(msAgo int64) models.AgentMessage {
	m := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "ok"})
	m.Timestamp = time.Now().Add(-time.Duration(msAgo) * time.Millisecond).UnixMilli()
	return m
}

// newToolResult builds a tool_result message with the given text length.
func newToolResult(id, name string, chars int) models.AgentMessage {
	return models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
		ToolCallID: id, Name: name,
		Content: []models.ContentPart{models.TextContent{Text: strings.Repeat("r", chars)}},
	})
}

// toolResultText extracts the inner text of a message's ToolResultContent
// (AgentMessage.Text only concatenates top-level TextContent, so a
// tool_result message's text lives inside the ToolResultContent shell and is
// invisible to Text()).
func toolResultText(m models.AgentMessage) string {
	for _, part := range m.Content {
		if tr, ok := part.(models.ToolResultContent); ok {
			return tr.Text()
		}
	}
	return m.Text()
}

// enabledMicroCompact returns a default-tuned config with the master switch on.
func enabledMicroCompact() MicroCompactConfig {
	cfg := DefaultMicroCompact()
	cfg.Enabled = true
	return cfg
}

// ── Default disabled ───────────────────────────────────────────────────────

// The shipped default must be disabled: Detect never advances and Apply never
// replaces, leaving messages untouched.
func TestMicroCompactDefaultDisabled(t *testing.T) {
	c := NewMicroCompactor(DefaultMicroCompact())
	if c.Enabled() {
		t.Fatal("default config must be disabled")
	}
	msgs := []models.AgentMessage{newToolResult("c1", "read", 5000)}
	if got := c.Detect(time.Now(), msgs, 1000, 900); got != -1 {
		t.Fatalf("disabled compactor must not advance cutoff, got %d", got)
	}
	if got := c.Apply(msgs); got != 0 {
		t.Fatalf("disabled compactor must not replace, got %d", got)
	}
	if !strings.Contains(toolResultText(msgs[0]), "rrr") {
		t.Fatal("disabled compactor must leave messages untouched")
	}
}

// ── Detect: three conditions AND ───────────────────────────────────────────

func TestMicroCompactDetectConditions(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.CacheMissedMs = 1000 // 1s so tests are fast
	cfg.KeepRecent = 2
	now := time.Now()
	// 4 messages so len(msgs)-KeepRecent(2) > 0, otherwise the cutoff never
	// advances and every case trivially returns -1.
	base := func(assistantMsAgo int64) []models.AgentMessage {
		return []models.AgentMessage{
			oldAssistantMsg(assistantMsAgo),
			newToolResult("c1", "read", 100),
			newToolResult("c2", "ls", 100),
			newToolResult("c3", "grep", 100),
		}
	}

	// All conditions met → advances (returns previous cutoff 0, not -1).
	c := NewMicroCompactor(cfg)
	cold := base(2000)
	if got := c.Detect(now, cold, 1000, 600); got != 0 {
		t.Fatalf("all conditions met should advance (prev=0), got %d", got)
	}
	// Disabled → never advances.
	c2 := NewMicroCompactor(cfg)
	c2.cfg.Enabled = false
	if got := c2.Detect(now, cold, 1000, 600); got != -1 {
		t.Fatal("disabled must not advance")
	}
	// Warm cache (assistant too recent) → no advance.
	c3 := NewMicroCompactor(cfg)
	if got := c3.Detect(now, base(500), 1000, 600); got != -1 {
		t.Fatal("warm cache must not advance")
	}
	// Usage below MinUsageRatio → no advance.
	c4 := NewMicroCompactor(cfg)
	if got := c4.Detect(now, cold, 1000, 400); got != -1 {
		t.Fatal("low usage must not advance")
	}
	// No assistant message → no advance.
	c5 := NewMicroCompactor(cfg)
	if got := c5.Detect(now, []models.AgentMessage{newToolResult("c1", "read", 100), newToolResult("c2", "ls", 100), newToolResult("c3", "grep", 100)}, 1000, 600); got != -1 {
		t.Fatal("no assistant message must not advance")
	}
}

// ── Cutoff advances monotonically ──────────────────────────────────────────

func TestMicroCompactCutoffAdvances(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.CacheMissedMs = 1000
	cfg.KeepRecent = 2
	c := NewMicroCompactor(cfg)
	now := time.Now()
	msgs := []models.AgentMessage{oldAssistantMsg(2000)}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, newToolResult(fmt.Sprintf("c%d", i), "read", 100))
	}
	// 6 messages, keep 2 → cutoff 4.
	if got := c.Detect(now, msgs, 1000, 600); got != 0 {
		t.Fatalf("first advance should return prev=0, got %d", got)
	}
	if c.cutoff != 4 {
		t.Fatalf("cutoff = %d, want 4", c.cutoff)
	}
	// Same message set again → no advance (cutoff never recedes).
	if got := c.Detect(now, msgs, 1000, 600); got != -1 {
		t.Fatalf("repeated detect must not advance, got %d", got)
	}
	// More messages → advances to 5.
	msgs2 := append(msgs, newToolResult("c9", "read", 100))
	if got := c.Detect(now, msgs2, 1000, 600); got != 4 {
		t.Fatalf("second advance should return prev=4, got %d", got)
	}
	if c.cutoff != 5 {
		t.Fatalf("cutoff = %d, want 5", c.cutoff)
	}
}

// ── Apply replacement rules ────────────────────────────────────────────────

func TestMicroCompactReplacesOldResults(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.KeepRecent = 2
	cfg.MinChars = 400
	c := NewMicroCompactor(cfg)
	c.cutoff = 4 // manual cutoff, bypassing Detect conditions
	msgs := []models.AgentMessage{
		newToolResult("c1", "read", 5000), // big → replaced
		models.UserMessage("keep me"),     // non-tool_result → kept
		newToolResult("c2", "ls", 100),    // small → kept
		newToolResult("c3", "grep", 800),  // big → replaced
		newToolResult("c4", "find", 900),  // past cutoff → kept
	}
	if got := c.Apply(msgs); got != 2 {
		t.Fatalf("replaced = %d, want 2", got)
	}
	if !strings.Contains(toolResultText(msgs[0]), "tool result cleared") {
		t.Fatalf("c1 should be trimmed, got %q", toolResultText(msgs[0]))
	}
	if !strings.Contains(toolResultText(msgs[2]), "rrr") {
		t.Fatal("small result must be kept")
	}
	if !strings.Contains(toolResultText(msgs[3]), "tool result cleared") {
		t.Fatal("c3 should be trimmed")
	}
	if !strings.Contains(toolResultText(msgs[4]), "rrr") {
		t.Fatal("post-cutoff result must be kept")
	}
}

// The trimmed result keeps its ToolResultContent shell (ToolCallID, Name,
// IsError), so the tool_use/tool_result pairing survives on the wire.
func TestMicroCompactPreservesPairing(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.KeepRecent = 1
	cfg.MinChars = 400
	c := NewMicroCompactor(cfg)
	c.cutoff = 3
	pair := toolPair("call_abc", 5000) // [tool_use, tool_result]
	msgs := append([]models.AgentMessage{}, pair...)
	msgs = append(msgs, newToolResult("call_xyz", "read", 700))

	if got := c.Apply(msgs); got != 2 {
		t.Fatalf("replaced = %d, want 2", got)
	}
	var tr models.ToolResultContent
	found := false
	for _, part := range msgs[1].Content {
		if r, ok := part.(models.ToolResultContent); ok {
			tr = r
			found = true
		}
	}
	if !found {
		t.Fatal("replaced result must stay a ToolResultContent")
	}
	if tr.ToolCallID != "call_abc" {
		t.Fatalf("ToolCallID = %q, want call_abc", tr.ToolCallID)
	}
	if tr.Name != "read" {
		t.Fatalf("Name = %q, want read", tr.Name)
	}
	if !strings.Contains(tr.Text(), "tool result cleared") {
		t.Fatalf("inner text should be the marker, got %q", tr.Text())
	}
	// The assistant tool_use message is untouched.
	if _, ok := msgs[0].Content[0].(models.ToolCallContent); !ok {
		t.Fatal("tool_use must be untouched")
	}
}

// ── Projection is lossless: blocks keep the full content ───────────────────

// BuildTurnRequest replaces old oversized tool results in the outgoing copy
// while the stored recent block stays byte-identical.
func TestMicroCompactProjectionIsLossless(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.KeepRecent = 2
	cfg.MinChars = 400
	m := NewManager(TokenBudget{MaxTotal: 1000, ReserveOutput: 0}, WithMicroCompact(cfg))
	// [old assistant, big tool, big tool, big user]: cache-cold, usage ~60%.
	m.ReplaceRecent([]models.AgentMessage{
		oldAssistantMsg(4_000_000),
		newToolResult("c1", "read", 5000),
		newToolResult("c2", "grep", 6000),
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: strings.Repeat("u", 2400)}),
	})
	req, err := m.BuildTurnRequest(models.ModelRef{ID: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var trimmed bool
	for _, msg := range req.Messages {
		if msg.Role == models.RoleToolResult && strings.Contains(toolResultText(msg), "tool result cleared") {
			trimmed = true
		}
	}
	if !trimmed {
		t.Fatal("outgoing request should contain a trimmed tool result")
	}
	recent, _ := m.GetBlock(BlockRecent, "recent")
	if !strings.Contains(toolResultText(recent.Messages[1]), "rrrr") {
		t.Fatal("stored block must keep the full tool result")
	}
	if !strings.Contains(toolResultText(recent.Messages[2]), "rrrr") {
		t.Fatal("stored block must keep the second full tool result")
	}
}

// ── Metrics: replaced count and embedded length ────────────────────────────

func TestMicroCompactMetrics(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.KeepRecent = 1
	cfg.MinChars = 100
	c := NewMicroCompactor(cfg)
	c.cutoff = 3
	msgs := []models.AgentMessage{
		newToolResult("c1", "read", 5000),
		newToolResult("c2", "ls", 200),
		newToolResult("c3", "grep", 700),
	}
	if got := c.Apply(msgs); got != 3 {
		t.Fatalf("replaced = %d, want 3", got)
	}
	// Marker embeds the original char count so volume information survives.
	if !strings.Contains(toolResultText(msgs[0]), "5000 chars") {
		t.Fatalf("marker should embed original length, got %q", toolResultText(msgs[0]))
	}
	if !strings.Contains(toolResultText(msgs[2]), "700 chars") {
		t.Fatalf("marker should embed original length, got %q", toolResultText(msgs[2]))
	}
}

// ── Reset on fold ──────────────────────────────────────────────────────────

// A committed full compaction resets the micro-compact cutoff: the fold rewrote
// the message indexes, so a stale cutoff would point at the wrong content.
func TestMicroCompactResetsOnFold(t *testing.T) {
	cfg := enabledMicroCompact()
	cfg.KeepRecent = 2
	cfg.MinChars = 400
	m := NewManager(TokenBudget{MaxTotal: 100000, ReserveOutput: 0},
		WithMicroCompact(cfg), WithSummarizer(stubSummarizer), WithMinRecent(1))
	m.SetSystemPrompt("sys " + strings.Repeat("x", 400))

	// Big recent block: old assistant + 10 large messages (~100k estimated
	// tokens) → reactive pressure guarantees a committed fold.
	big := make([]models.AgentMessage, 0, 11)
	big = append(big, oldAssistantMsg(4_000_000))
	for i := 0; i < 10; i++ {
		role := models.RoleUser
		if i%2 == 1 {
			role = models.RoleAssistant
		}
		big = append(big, models.NewAgentMessage(role, models.TextContent{Text: strings.Repeat("m", 40000)}))
	}
	m.ReplaceRecent(big)

	// Drive the cutoff up via Detect. The last assistant message must be
	// cache-cold too, so give every assistant message an old timestamp.
	for i := range big {
		if big[i].Role == models.RoleAssistant {
			big[i].Timestamp = time.Now().Add(-time.Duration(4_000_000) * time.Millisecond).UnixMilli()
		}
	}
	if got := m.microCompact.Detect(time.Now(), big, 100000, 90000); got != 0 {
		t.Fatalf("detect should advance to a nonzero cutoff, got %d", got)
	}
	if m.microCompact.cutoff <= 0 {
		t.Fatalf("expected a nonzero cutoff before fold, got %d", m.microCompact.cutoff)
	}

	_, res, err := m.MaybeCompactLeveled(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed {
		t.Fatal("expected a committed fold to test the reset")
	}
	if m.microCompact.cutoff != 0 {
		t.Fatalf("cutoff after fold = %d, want 0", m.microCompact.cutoff)
	}
}
