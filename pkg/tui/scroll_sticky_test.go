package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func fillBlocks(m *Model, n int) {
	for i := 0; i < n; i++ {
		m.blocks = append(m.blocks, block{kind: components.BlockAssistant, id: fmt.Sprintf("a%d", i), raw: fmt.Sprintf("answer %d", i)})
	}
	m.components = componentsFromBlocks(m.blocks)
	m.updateSizes()
}

// Wheel-scroll up must materialize the virtual window for the new offset:
// without a rebuild, the lines above the old window are blank placeholders.
func TestWheelScrollUpShowsEarlierBlocks(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	fillBlocks(m, 60)

	if !m.viewport.AtBottom() {
		t.Fatal("expected to start at bottom")
	}
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	view := m.viewport.View()
	if !strings.Contains(view, "answer") {
		t.Fatalf("scrolled-up view shows blank placeholders instead of history:\n%q", view)
	}
}

// Sticky bottom: while streaming, a user who scrolled up must not be yanked
// back to the bottom by the next delta.
func TestStreamDoesNotYankScrolledUpUser(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	fillBlocks(m, 60)
	m.sched.minInterval = 0 // force immediate flush on stream events

	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "live"}})

	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	y := m.viewport.YOffset
	if m.viewport.AtBottom() {
		t.Fatal("wheel up should leave the bottom")
	}

	m.handleEvent(events.MessageUpdateEvent{Delta: "more text"})
	if m.viewport.YOffset != y {
		t.Fatalf("delta yanked the scrolled-up user: YOffset %d -> %d", y, m.viewport.YOffset)
	}
}

// At the bottom, streaming keeps pinning to the tail.
func TestStreamPinsWhenAtBottom(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	fillBlocks(m, 60)
	m.sched.minInterval = 0

	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "live"}})
	for range 10 {
		m.handleEvent(events.MessageUpdateEvent{Delta: "line\nline\nline\n"})
	}
	if !m.viewport.AtBottom() {
		t.Fatal("streaming should pin to the bottom while the user is at the bottom")
	}
}

// Submitting a prompt scrolls history back to the bottom.
func TestSubmitScrollsToBottom(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	fillBlocks(m, 60)

	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.viewport.AtBottom() {
		t.Fatal("wheel up should leave the bottom")
	}
	m.input.textarea.SetValue("next question")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.viewport.AtBottom() {
		t.Fatal("submitting a prompt should scroll to the bottom")
	}
}
