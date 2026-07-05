package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

type fakeProgramSender struct {
	msgs chan tea.Msg
}

func (f *fakeProgramSender) Send(msg tea.Msg) {
	f.msgs <- msg
}

func TestTuiConfirmBlocksUntilResponse(t *testing.T) {
	sender := &fakeProgramSender{msgs: make(chan tea.Msg, 1)}
	confirm := &tuiConfirm{program: sender}

	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "ls"}},
	}

	resultCh := make(chan struct {
		allow bool
		err   error
	})
	go func() {
		allow, err := confirm.Confirm(context.Background(), info)
		resultCh <- struct {
			allow bool
			err   error
		}{allow, err}
	}()

	var msg tea.Msg
	select {
	case msg = <-sender.msgs:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for confirm request")
	}

	req, ok := msg.(confirmRequestMsg)
	if !ok {
		t.Fatalf("expected confirmRequestMsg, got %T", msg)
	}
	if req.req.info.ToolCall.Name != "bash" {
		t.Fatalf("expected bash tool, got %s", req.req.info.ToolCall.Name)
	}

	// Unblock the waiting confirmation.
	req.req.resp <- confirmResult{allow: true}

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
		if !r.allow {
			t.Fatal("expected allow=true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for confirm result")
	}
}

func TestConfirmPanelSelectionAndDecision(t *testing.T) {
	p := &confirmPanel{}
	p.show(agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "ls"}},
	}, make(chan confirmResult, 1))

	if !p.visible {
		t.Fatal("panel should be visible")
	}
	if p.selected != 0 {
		t.Fatalf("default selection should be Allow(0), got %d", p.selected)
	}

	p.next()
	if p.selected != 1 {
		t.Fatalf("next should move to Deny(1), got %d", p.selected)
	}
	p.prev()
	if p.selected != 0 {
		t.Fatalf("prev should move back to Allow(0), got %d", p.selected)
	}

	if !p.confirm() {
		t.Fatal("confirm on Allow should return true")
	}
	p.next()
	if p.confirm() {
		t.Fatal("confirm on Deny should return false")
	}
}

func TestConfirmPanelRendersAsBottomStrip(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.blocks = append(m.blocks, block{kind: blockUser, raw: "hello"})
	m.updateSizes()

	resp := make(chan confirmResult, 1)
	m2, _ := m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "ls"}}},
		resp: resp,
	}})
	mm := m2.(*Model)

	view := mm.View()
	if !strings.Contains(view, "hello") {
		t.Fatalf("view should still show log content, missing 'hello':\n%s", view)
	}
	if !strings.Contains(view, "Permission request: bash") {
		t.Fatalf("view missing permission prompt:\n%s", view)
	}
	if !strings.Contains(view, "Allow") {
		t.Fatalf("view missing Allow option:\n%s", view)
	}
	if !strings.Contains(view, "Deny") {
		t.Fatalf("view missing Deny option:\n%s", view)
	}
	if strings.Contains(view, "Type a message") {
		t.Fatalf("input box should be hidden while confirming:\n%s", view)
	}
}

func TestConfirmPanelArrowSelection(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	resp := make(chan confirmResult, 1)
	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: resp,
	}})

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	mm := m2.(*Model)
	if mm.confirm.selected != 1 {
		t.Fatalf("right arrow should select Deny, got %d", mm.confirm.selected)
	}

	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyLeft})
	mm = m3.(*Model)
	if mm.confirm.selected != 0 {
		t.Fatalf("left arrow should select Allow, got %d", mm.confirm.selected)
	}
}

func TestConfirmPanelEnterDecision(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	resp := make(chan confirmResult, 1)
	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: resp,
	}})

	// Move to Deny and confirm.
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command to send confirmResponseMsg")
	}

	msg := cmd()
	m3, _ := m2.Update(msg)
	mm := m3.(*Model)
	if mm.state != stateProcessing {
		t.Fatalf("expected stateProcessing after Enter, got %v", mm.state)
	}
	cmsg, ok := msg.(confirmResponseMsg)
	if !ok {
		t.Fatalf("expected confirmResponseMsg, got %T", msg)
	}
	if cmsg.allow {
		t.Fatal("Deny selection should produce allow=false")
	}
}

func TestConfirmPanelCanScrollLog(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 10
	m.updateSizes()

	// Fill the viewport with enough content to scroll.
	for i := 0; i < 50; i++ {
		m.blocks = append(m.blocks, block{kind: blockUser, raw: fmt.Sprintf("line %d", i)})
	}
	m.rebuildViewport()

	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: make(chan confirmResult, 1),
	}})

	before := m.viewport.YOffset
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	mm := m2.(*Model)
	if mm.viewport.YOffset == before {
		t.Fatal("expected viewport to scroll up while confirming")
	}
}

func TestConfirmPanelStateTransitions(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)

	resp := make(chan confirmResult, 1)
	info := agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
	}

	m2, _ := m.Update(confirmRequestMsg{req: confirmRequest{info: info, resp: resp}})
	mm := m2.(*Model)
	if mm.state != stateConfirm {
		t.Fatalf("expected stateConfirm, got %v", mm.state)
	}
	if !mm.confirm.visible {
		t.Fatal("expected confirm panel visible")
	}

	m3, _ := mm.Update(confirmResponseMsg{allow: true})
	mm = m3.(*Model)
	if mm.state != stateProcessing {
		t.Fatalf("expected stateProcessing, got %v", mm.state)
	}
	if mm.confirm.visible {
		t.Fatal("expected confirm panel hidden")
	}
}
