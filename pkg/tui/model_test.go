package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/testutil"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func newTestModel() (*Model, *testutil.FakeAgent, *testutil.FakeSession) {
	bus := events.New()
	ag := &testutil.FakeAgent{}
	sess := &testutil.FakeSession{ID: "abc123"}
	store := &testutil.FakeSessionStore{}
	m := NewModel(bus, ag, sess, store, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	return m, ag, sess
}

func TestStatusTextShowsCapabilities(t *testing.T) {
	m, _, _ := newTestModel()
	if strings.Contains(m.statusText(), "caps:") {
		t.Fatal("expected no caps segment before capabilities are set")
	}
	m.SetCapabilities([]string{"tools", "vision"})
	out := m.statusText()
	if !strings.Contains(out, "caps:") || !strings.Contains(out, "tools") || !strings.Contains(out, "vision") {
		t.Fatalf("expected capabilities in status, got %q", out)
	}
}

func TestModelHandlesUserInput(t *testing.T) {
	m, agent, _ := newTestModel()
	m.state = stateInput

	m.input.textarea.SetValue("hello")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(*Model)

	if cmd == nil {
		t.Fatal("expected command after enter")
	}

	var userBlocks int
	var last string
	for _, b := range m2.blocks {
		if b.kind == components.BlockUser {
			userBlocks++
			last = b.raw
		}
	}
	if userBlocks != 1 {
		t.Fatalf("expected 1 user block, got %d", userBlocks)
	}
	if last != "hello" {
		t.Fatalf("expected raw %q, got %q", "hello", last)
	}
	if m2.state != stateProcessing {
		t.Fatalf("expected stateProcessing, got %v", m2.state)
	}
	if len(agent.Prompts) != 0 {
		// Prompt runs asynchronously via tea.Cmd, not yet executed.
		t.Fatalf("expected prompts to be empty before cmd exec, got %d", len(agent.Prompts))
	}
}

func TestModelHandlesEvents(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing

	msg := models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "Hi"})
	updated, _ := m.Update(EventMsg{Event: events.MessageEndEvent{
		Base:    events.Base{Type: events.MessageEnd, Turn: 0},
		Message: msg,
	}})
	m2 := updated.(*Model)

	var n int
	var got string
	for _, b := range m2.blocks {
		if b.kind == components.BlockAssistant {
			n++
			got = b.raw
		}
	}
	if n != 1 {
		t.Fatalf("expected 1 assistant block, got %d", n)
	}
	if got != "Hi" {
		t.Fatalf("expected 'Hi', got %s", got)
	}
}

func TestCtrlOTogglesToolExpansionInInputState(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = append(m.blocks, block{
		kind:       components.BlockTool,
		toolName:   "bash",
		toolArgs:   `{"command":"ls"}`,
		toolResult: "line1\nline2\nline3\nline4",
	})

	if m.toolsExpanded {
		t.Fatal("expected toolsExpanded to start false")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m2 := updated.(*Model)
	if !m2.toolsExpanded {
		t.Fatal("expected Ctrl+O to toggle toolsExpanded in input state")
	}

	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m3 := updated.(*Model)
	if m3.toolsExpanded {
		t.Fatal("expected second Ctrl+O to toggle toolsExpanded off")
	}
}

func TestModelViewNotEmpty(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestFormatArgs(t *testing.T) {
	args := map[string]any{"path": "main.go", "line": 42}
	out := FormatArgs(args)
	if out == "" {
		t.Fatal("expected non-empty args")
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "line") {
		t.Fatalf("expected full JSON args, got %q", out)
	}
	if strings.Contains(out, "...") {
		t.Fatalf("expected full args without truncation, got %q", out)
	}

	// Long argument maps should not be truncated.
	longArgs := map[string]any{"command": strings.Repeat("echo hello; ", 20)}
	longOut := FormatArgs(longArgs)
	if strings.Contains(longOut, "...") {
		t.Fatalf("expected long args without truncation, got %q", longOut)
	}
}

// Ensure concrete types satisfy the TUI interfaces.
var (
	_ AgentRunner   = (*fakeAgent)(nil)
	_ SessionWriter = (*fakeSession)(nil)
	_ SessionWriter = (*session.Session)(nil)
)

func TestParseModeCommand(t *testing.T) {
	cases := []struct {
		input string
		name  string
		ok    bool
	}{
		{"/mode review", "review", true},
		{"/mode  plan", "plan", true},
		{"/mode", "", true},
		{"hello", "", false},
		{"/modeauto", "", false},
	}
	for _, c := range cases {
		name, ok := parseModeCommand(c.input)
		if ok != c.ok || name != c.name {
			t.Errorf("parseModeCommand(%q) = (%q, %v), want (%q, %v)", c.input, name, ok, c.name, c.ok)
		}
	}
}

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		input string
		cmd   string
		args  string
		ok    bool
	}{
		{"/help", "help", "", true},
		{"/?", "?", "", true},
		{"/tools", "tools", "", true},
		{"/mode review", "mode", "review", true},
		{"/mode  plan", "mode", "plan", true},
		{"/mode", "mode", "", true},
		{"hello", "", "", false},
		{"/modeauto", "modeauto", "", true},
	}
	for _, c := range cases {
		cmd, ok := parseSlashCommand(c.input)
		if ok != c.ok || cmd.Name != c.cmd || cmd.Args != c.args {
			t.Errorf("parseSlashCommand(%q) = (%q, %q, %v), want (%q, %q, %v)", c.input, cmd.Name, cmd.Args, ok, c.cmd, c.args, c.ok)
		}
	}
}

func TestShiftUpFocusesLastBlock(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = []block{
		{kind: components.BlockUser, raw: "hello"},
		{kind: components.BlockAssistant, id: "a1", raw: "hi"},
		{kind: components.BlockSystem, raw: "sys"},
		{kind: components.BlockTool, id: "t1", toolName: "bash", toolResult: "out"},
	}
	m.components = componentsFromBlocks(m.blocks)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m2 := updated.(*Model)
	if m2.focusedBlockIndex != 3 {
		t.Fatalf("expected focus on last interactive block (3), got %d", m2.focusedBlockIndex)
	}
}

func TestShiftUpDownClampsFocus(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = []block{
		{kind: components.BlockAssistant, id: "a1", raw: "hi"},
		{kind: components.BlockTool, id: "t1", toolName: "bash", toolResult: "out"},
	}
	m.components = componentsFromBlocks(m.blocks)

	// Focus first interactive block.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
	m2 := updated.(*Model)
	if m2.focusedBlockIndex != 0 {
		t.Fatalf("expected focus on first interactive block (0), got %d", m2.focusedBlockIndex)
	}

	// Clamped at the first block.
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	m3 := updated.(*Model)
	if m3.focusedBlockIndex != 0 {
		t.Fatalf("expected focus to clamp at 0, got %d", m3.focusedBlockIndex)
	}

	// Move to last block and clamp there.
	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
	m4 := updated.(*Model)
	if m4.focusedBlockIndex != 1 {
		t.Fatalf("expected focus on last interactive block (1), got %d", m4.focusedBlockIndex)
	}
	updated, _ = m4.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
	m5 := updated.(*Model)
	if m5.focusedBlockIndex != 1 {
		t.Fatalf("expected focus to clamp at 1, got %d", m5.focusedBlockIndex)
	}
}

func TestCtrlETogglesFocusedAssistant(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = []block{
		{kind: components.BlockAssistant, id: "a1", raw: "hi", thinking: "step one\nstep two"},
	}
	m.components = componentsFromBlocks(m.blocks)
	m.focusedBlockIndex = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m2 := updated.(*Model)
	if !m2.blocks[0].expanded {
		t.Fatal("expected block.expanded to be true after Ctrl+E")
	}
	ac, ok := m2.components[0].(*components.AssistantComponent)
	if !ok {
		t.Fatalf("expected *components.AssistantComponent, got %T", m2.components[0])
	}
	if !ac.Expanded() {
		t.Fatal("expected assistant component Expanded() to be true")
	}
}

func TestCtrlETogglesFocusedTool(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = []block{
		{kind: components.BlockTool, id: "t1", toolName: "bash", toolArgs: `{"command":"ls"}`, toolResult: "line1\nline2\nline3\nline4"},
	}
	m.components = componentsFromBlocks(m.blocks)
	m.focusedBlockIndex = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m2 := updated.(*Model)
	if !m2.blocks[0].expanded {
		t.Fatal("expected block.expanded to be true after Ctrl+E")
	}
	tr, ok := m2.components[0].(*components.ToolResultComponent)
	if !ok {
		t.Fatalf("expected *components.ToolResultComponent, got %T", m2.components[0])
	}
	if !tr.Expanded() {
		t.Fatal("expected tool component Expanded() to be true")
	}
}

func TestEscClearsFocusInInputState(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "hi"}}
	m.components = componentsFromBlocks(m.blocks)
	m.focusedBlockIndex = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := updated.(*Model)
	if m2.focusedBlockIndex != -1 {
		t.Fatalf("expected focus cleared, got %d", m2.focusedBlockIndex)
	}
}

func TestEscClearsFocusBeforeAbortInProcessingState(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "hi"}}
	m.components = componentsFromBlocks(m.blocks)
	m.focusedBlockIndex = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := updated.(*Model)
	if m2.focusedBlockIndex != -1 {
		t.Fatalf("expected first Esc to clear focus, got %d", m2.focusedBlockIndex)
	}

	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := updated.(*Model)
	if m3.focusedBlockIndex != -1 {
		t.Fatalf("expected focus to remain -1 after abort, got %d", m3.focusedBlockIndex)
	}
	// The abort path adds a system "interrupted" line.
	var found bool
	for _, b := range m3.blocks {
		if b.kind == components.BlockSystem && strings.Contains(b.raw, "interrupted") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'interrupted' system block after second Esc")
	}
}

func TestCtrlEDoesNothingWhenNoFocus(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "hi"}}
	m.components = componentsFromBlocks(m.blocks)
	m.focusedBlockIndex = -1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m2 := updated.(*Model)
	if m2.focusedBlockIndex != -1 {
		t.Fatalf("expected no focus change, got %d", m2.focusedBlockIndex)
	}
}
