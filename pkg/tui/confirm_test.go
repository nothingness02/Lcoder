package tui

import (
	"context"
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
