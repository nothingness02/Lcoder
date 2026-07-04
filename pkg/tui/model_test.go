package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/task"
)

type fakeAgent struct {
	prompts []models.AgentMessage
	msgs    []models.AgentMessage
	mode    string

	switchedModel  models.ModelRef
	switchedBudget contextmgr.TokenBudget
}

func (f *fakeAgent) Prompt(_ context.Context, msg models.AgentMessage) error {
	f.prompts = append(f.prompts, msg)
	return nil
}

func (f *fakeAgent) Continue(_ context.Context) error       { return nil }
func (f *fakeAgent) AllMessages() []models.AgentMessage     { return f.msgs }
func (f *fakeAgent) SetMessages(msgs []models.AgentMessage) { f.msgs = msgs }
func (f *fakeAgent) Stats() map[string]int                  { return nil }
func (f *fakeAgent) Mode() string {
	if f.mode == "" {
		return "code"
	}
	return f.mode
}
func (f *fakeAgent) SetUserConfirm(uc agent.UserConfirmation) {}
func (f *fakeAgent) Steer(models.AgentMessage)                {}
func (f *fakeAgent) Abort()                                   {}
func (f *fakeAgent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	f.switchedModel = ref
	f.switchedBudget = budget
}
func (f *fakeAgent) TaskManager() *task.Manager { return nil }
func (f *fakeAgent) WithMode(mode string) AgentRunner {
	f.mode = mode
	return f
}

type fakeSession struct {
	id       string
	messages []models.AgentMessage
}

func (f *fakeSession) Append(msg models.AgentMessage) error {
	f.messages = append(f.messages, msg)
	return nil
}
func (f *fakeSession) SessionID() string { return f.id }

type fakeSessionStore struct{}

func (f *fakeSessionStore) List(cwd string) ([]session.Session, error) { return nil, nil }
func (f *fakeSessionStore) LoadByID(cwd, id string) (*session.Session, error) {
	return nil, nil
}

func newTestModel() (*Model, *fakeAgent, *fakeSession) {
	bus := events.New()
	agent := &fakeAgent{}
	sess := &fakeSession{id: "abc123"}
	store := &fakeSessionStore{}
	m := NewModel(bus, agent, sess, store, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	return m, agent, sess
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
		if b.kind == blockUser {
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
	if len(agent.prompts) != 0 {
		// Prompt runs asynchronously via tea.Cmd, not yet executed.
		t.Fatalf("expected prompts to be empty before cmd exec, got %d", len(agent.prompts))
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
		if b.kind == blockAssistant {
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

func TestModelViewNotEmpty(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestToolResultText(t *testing.T) {
	result := models.NewToolExecutionResultText("hello world this is a long result")
	out := toolResultText(result)
	if out == "" {
		t.Fatal("expected non-empty result text")
	}
}

func TestFormatArgs(t *testing.T) {
	args := map[string]any{"path": "main.go", "line": 42}
	out := FormatArgs(args)
	if out == "" {
		t.Fatal("expected non-empty args")
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
