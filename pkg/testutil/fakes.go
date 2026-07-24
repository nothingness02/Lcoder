// Package testutil provides reusable test fixtures for Lcoder packages.
package testutil

import (
	"context"
	"strconv"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/task"
)

// FakeAgent is a minimal implementation of agent.Runner (and ModeSwitcher) for
// TUI and executor tests. All fields are exported so tests can program or
// inspect behavior.
type FakeAgent struct {
	Prompts        []models.AgentMessage
	Messages       []models.AgentMessage
	ModeName       string
	TaskMgr        *task.Manager
	SwitchedModel  models.ModelRef
	SwitchedBudget contextmgr.TokenBudget
	SessionIDVal   string
	// StatsVal, when non-nil, is returned by Stats so tests can program the
	// context-budget figures the TUI status line consumes.
	StatsVal map[string]int
}

func (f *FakeAgent) Prompt(_ context.Context, msg models.AgentMessage) error {
	f.Prompts = append(f.Prompts, msg)
	return nil
}

func (f *FakeAgent) Continue(_ context.Context) error   { return nil }
func (f *FakeAgent) AllMessages() []models.AgentMessage { return f.Messages }
func (f *FakeAgent) SetMessages(msgs []models.AgentMessage) {
	f.Messages = msgs
}
func (f *FakeAgent) Stats() map[string]int { return f.StatsVal }
func (f *FakeAgent) Mode() string {
	if f.ModeName == "" {
		return "code"
	}
	return f.ModeName
}
func (f *FakeAgent) SessionID() string   { return f.SessionIDVal }
func (f *FakeAgent) SetSessionID(id string) { f.SessionIDVal = id }
func (f *FakeAgent) SetUserConfirm(uc agent.UserConfirmation) {}
func (f *FakeAgent) Steer(models.AgentMessage)                {}
func (f *FakeAgent) Abort()                                   {}
func (f *FakeAgent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	f.SwitchedModel = ref
	f.SwitchedBudget = budget
}
func (f *FakeAgent) TaskManager() *task.Manager {
	return f.TaskMgr
}

// WithMode implements agent.ModeSwitcher by recording the requested mode and
// returning itself. It satisfies the interface TUI uses for mode switches.
func (f *FakeAgent) WithMode(mode string) agent.Runner {
	f.ModeName = mode
	return f
}

var (
	_ agent.Runner     = (*FakeAgent)(nil)
	_ agent.ModeSwitcher = (*FakeAgent)(nil)
)

// FakeSession is a minimal implementation of the TUI SessionWriter interface.
type FakeSession struct {
	ID       string
	Messages []models.AgentMessage
}

func (f *FakeSession) Append(msg models.AgentMessage) error {
	f.Messages = append(f.Messages, msg)
	return nil
}
func (f *FakeSession) SessionID() string { return f.ID }

// FakeSessionStore is a minimal implementation of the TUI SessionStore interface.
type FakeSessionStore struct {
	Sessions []*session.Session
	Session  *session.Session
	Err      error
	// Dir, when set, causes Create to delegate to a real session store in that
	// directory so tests can exercise new-session creation end-to-end.
	Dir     string
	Created []*session.Session
}

func (f *FakeSessionStore) List(cwd string) ([]*session.Session, error) {
	return f.Sessions, f.Err
}
func (f *FakeSessionStore) LoadByID(cwd, id string) (*session.Session, error) {
	return f.Session, f.Err
}
func (f *FakeSessionStore) Create(cwd string) (*session.Session, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if f.Dir != "" {
		s, err := session.NewStore(f.Dir).Create(cwd)
		if err != nil {
			return nil, err
		}
		f.Created = append(f.Created, s)
		return s, nil
	}
	s := f.Session
	if s == nil {
		s = &session.Session{ID: "fake-created-" + strconv.Itoa(len(f.Created)), CWD: cwd}
	}
	f.Created = append(f.Created, s)
	return s, nil
}
