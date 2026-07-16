package desktop

import (
	"context"

	"github.com/lcoder/lcoder/pkg/events"
)

// SessionPersister mirrors runtime state into the active session store.
type SessionPersister struct {
	runtime *Runtime
	unsub   func()
}

// NewSessionPersister creates and immediately subscribes a persister.
func NewSessionPersister(runtime *Runtime, bus *events.Bus) *SessionPersister {
	sp := &SessionPersister{runtime: runtime}
	sp.unsub = bus.Subscribe(sp.onEvent)
	return sp
}

// Close unsubscribes from the event bus.
func (sp *SessionPersister) Close() {
	if sp.unsub != nil {
		sp.unsub()
	}
}

func (sp *SessionPersister) onEvent(ctx context.Context, ev events.Event) error {
	switch e := ev.(type) {
	case events.CompactionCommittedEvent:
		if !e.Degraded && e.Summary != "" {
			_ = sp.runtime.Session.AppendCompactionEntry(e.Summary, e.FirstKeptID, e.TokensBefore)
			_ = sp.runtime.Session.AppendMissing(sp.runtime.Agent.AllMessages())
		}
	case events.AgentEndEvent:
		_ = sp.runtime.Session.AppendMissing(sp.runtime.Agent.AllMessages())
		_ = sp.runtime.Session.Save()
	case events.MessageEndEvent, events.ToolExecutionEndEvent:
		_ = sp.runtime.Session.Save()
	}
	return nil
}
