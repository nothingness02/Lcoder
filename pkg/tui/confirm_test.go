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
	"github.com/lcoder/lcoder/pkg/tui/components"
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
	req.req.resp <- confirmResult{allow: true, scope: agent.ScopeOnce}

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
		t.Fatalf("default selection should be Deny(0), got %d", p.selected)
	}
	wantLabels := []string{"Deny", "Once", "Session", "Project (bash: ls *)", "Global (bash: ls *)"}
	if len(p.options) != len(wantLabels) {
		t.Fatalf("expected %v options, got %v", wantLabels, p.options)
	}
	for i, opt := range p.options {
		if opt.label != wantLabels[i] {
			t.Fatalf("option %d: expected %q, got %q", i, wantLabels[i], opt.label)
		}
	}

	p.next()
	if p.selected != 1 {
		t.Fatalf("next should move to Once(1), got %d", p.selected)
	}
	p.prev()
	if p.selected != 0 {
		t.Fatalf("prev should move back to Deny(0), got %d", p.selected)
	}

	res := p.confirm()
	if res.Allow {
		t.Fatal("confirm on Deny should return allow=false")
	}
	if res.Scope != agent.ScopeDeny {
		t.Fatalf("confirm on Deny should return scope=ScopeDeny, got %v", res.Scope)
	}
	p.next()
	res = p.confirm()
	if !res.Allow || res.Scope != agent.ScopeOnce {
		t.Fatalf("confirm on Once should return allow=true scope=ScopeOnce, got %v", res)
	}
	p.next()
	res = p.confirm()
	if !res.Allow || res.Scope != agent.ScopeSession {
		t.Fatalf("confirm on Session should return allow=true scope=ScopeSession, got %v", res)
	}
}

func TestConfirmPanelRendersAsBottomStrip(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)
	m.width = 80
	m.height = 24
	m.blocks = append(m.blocks, block{kind: components.BlockUser, raw: "hello"})
	m.components = componentsFromBlocks(m.blocks)
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
	for _, opt := range []string{"Deny", "Once", "Session", "Project (bash: ls *)", "Global (bash: ls *)"} {
		if !strings.Contains(view, opt) {
			t.Fatalf("view missing option %q:\n%s", opt, view)
		}
	}
	if strings.Contains(view, "Type a message") {
		t.Fatalf("input box should be hidden while confirming:\n%s", view)
	}
}

func TestConfirmPanelArrowSelection(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)
	resp := make(chan confirmResult, 1)
	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: resp,
	}})

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	mm := m2.(*Model)
	if mm.confirm.selected != 1 {
		t.Fatalf("right arrow should select Once, got %d", mm.confirm.selected)
	}

	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyLeft})
	mm = m3.(*Model)
	if mm.confirm.selected != 0 {
		t.Fatalf("left arrow should select Deny, got %d", mm.confirm.selected)
	}
}

func TestConfirmPanelEnterDecision(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)
	resp := make(chan confirmResult, 1)
	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: resp,
	}})

	// Move to Once and confirm.
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
	if !cmsg.allow {
		t.Fatal("Once selection should produce allow=true")
	}
	if cmsg.scope != agent.ScopeOnce {
		t.Fatalf("Once selection should produce scope=ScopeOnce, got %v", cmsg.scope)
	}
}

func TestConfirmPanelUltraDestructiveHidesGlobal(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)
	resp := make(chan confirmResult, 1)
	m2, _ := m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "rm -rf /"}}},
		resp: resp,
	}})
	mm := m2.(*Model)

	if !mm.confirm.ultra {
		t.Fatal("expected ultra-destructive panel")
	}
	for _, opt := range mm.confirm.options {
		if opt.scope == agent.ScopeGlobal {
			t.Fatal("global allow should not be offered for ultra-destructive commands")
		}
	}
}

func TestConfirmPanelCanScrollLog(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)
	m.width = 80
	m.height = 10
	m.updateSizes()

	// Fill the viewport with enough content to scroll.
	for i := 0; i < 50; i++ {
		m.blocks = append(m.blocks, block{kind: components.BlockUser, raw: fmt.Sprintf("line %d", i)})
	}
	m.components = componentsFromBlocks(m.blocks)
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
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)

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

	m3, _ := mm.Update(confirmResponseMsg{allow: true, scope: agent.ScopeOnce})
	mm = m3.(*Model)
	if mm.state != stateProcessing {
		t.Fatalf("expected stateProcessing, got %v", mm.state)
	}
	if mm.confirm.visible {
		t.Fatal("expected confirm panel hidden")
	}
}
