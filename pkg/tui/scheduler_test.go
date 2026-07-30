package tui

import (
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// startStreaming drives the model into streaming state and returns the
// rebuild count after the (immediate) message-start rebuild.
func startStreaming(t *testing.T, m *Model) int {
	t.Helper()
	m.handleEvent(events.MessageStartEvent{Message: models.AgentMessage{Role: models.RoleAssistant, ID: "s1"}})
	return m.rebuilds
}

func TestStreamRenderCoalesces(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	base := time.Now()
	now := base
	m.sched.now = func() time.Time { return now }
	m.sched.minInterval = 33 * time.Millisecond

	start := startStreaming(t, m)

	// A burst of deltas within one interval rebuilds nothing new; it marks
	// dirty and schedules exactly one flush tick.
	for range 5 {
		m.handleEvent(events.MessageUpdateEvent{Delta: "x"})
	}
	if m.rebuilds != start {
		t.Fatalf("deltas within minInterval should not rebuild, rebuilds=%d start=%d", m.rebuilds, start)
	}
	if !m.sched.dirty {
		t.Fatal("coalesced deltas should leave the scheduler dirty")
	}

	// The pending tick flushes exactly once.
	now = now.Add(40 * time.Millisecond)
	m.Update(frameTickMsg{})
	if m.rebuilds != start+1 {
		t.Fatalf("tick should flush exactly one rebuild, rebuilds=%d want %d", m.rebuilds, start+1)
	}
	if m.sched.dirty {
		t.Fatal("flush should clear the dirty flag")
	}
}

func TestStreamRenderImmediateAfterInterval(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	base := time.Now()
	now := base
	m.sched.now = func() time.Time { return now }
	m.sched.minInterval = 33 * time.Millisecond

	start := startStreaming(t, m)

	now = now.Add(50 * time.Millisecond)
	cmd := m.handleEvent(events.MessageUpdateEvent{Delta: "x"})
	if m.rebuilds != start+1 {
		t.Fatalf("delta past minInterval should rebuild immediately, rebuilds=%d want %d", m.rebuilds, start+1)
	}
	if cmd != nil {
		t.Fatal("immediate flush should not schedule a tick")
	}
}

func TestTerminalEventsFlushImmediately(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	base := time.Now()
	now := base
	m.sched.now = func() time.Time { return now }
	m.sched.minInterval = time.Hour // extreme: any coalescing would be visible

	start := startStreaming(t, m)

	// Within the (huge) interval a delta only marks dirty...
	m.handleEvent(events.MessageUpdateEvent{Delta: "partial"})
	if m.rebuilds != start {
		t.Fatal("delta should be coalesced")
	}
	// ...but MessageEnd lands synchronously, no tick needed.
	final := models.AgentMessage{Role: models.RoleAssistant, ID: "s1"}
	m.handleEvent(events.MessageEndEvent{Message: final})
	if m.rebuilds != start+1 {
		t.Fatalf("MessageEnd must rebuild immediately, rebuilds=%d want %d", m.rebuilds, start+1)
	}
}

func TestTermProfile(t *testing.T) {
	cases := []struct {
		name       string
		termProg   string
		fpsEnv     string
		want       time.Duration
	}{
		{"default", "", "", 33 * time.Millisecond},
		{"vscode degraded", "vscode", "", 100 * time.Millisecond},
		{"fps override", "", "60", time.Second / 60},
		{"fps override beats vscode", "vscode", "30", time.Second / 30},
		{"invalid fps falls back", "", "abc", 33 * time.Millisecond},
		{"zero fps falls back", "", "0", 33 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", c.termProg)
			t.Setenv("LCODER_TUI_FPS", c.fpsEnv)
			if got := termProfile(); got != c.want {
				t.Errorf("termProfile() = %v, want %v", got, c.want)
			}
		})
	}
}
