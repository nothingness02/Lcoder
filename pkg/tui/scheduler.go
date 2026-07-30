package tui

import (
	"os"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// frameTickMsg fires the scheduled stream flush (see requestStreamRender).
type frameTickMsg struct{}

// frameScheduler coalesces high-frequency stream repaints into at most one
// viewport rebuild per minInterval: throttle + coalesce (pi-tui's
// scheduleRender), not debounce — a debounced stream tail would stay stale
// for as long as deltas keep arriving.
type frameScheduler struct {
	dirty       bool
	minInterval time.Duration
	lastFlush   time.Time
	timerActive bool
	now         func() time.Time // injectable for tests
}

// termProfile returns the stream repaint interval for the detected terminal.
// VSCode's integrated terminal renders slowly under rapid ANSI bursts
// (tui-known-issues #3), so it gets a degraded 10fps; LCODER_TUI_FPS
// overrides everything for diagnosis and regression checks.
func termProfile() time.Duration {
	if v := os.Getenv("LCODER_TUI_FPS"); v != "" {
		if fps, err := strconv.Atoi(v); err == nil && fps > 0 {
			return time.Second / time.Duration(fps)
		}
	}
	if os.Getenv("TERM_PROGRAM") == "vscode" {
		return 100 * time.Millisecond
	}
	return 33 * time.Millisecond
}

// now reports the scheduler clock. Tests build zero-value Models directly
// (bypassing NewModel), so a nil clock falls back to the wall clock.
func (m *Model) now() time.Time {
	if m.sched.now == nil {
		return time.Now()
	}
	return m.sched.now()
}

// requestStreamRender rebuilds the viewport immediately when the last flush
// is at least minInterval old; otherwise it marks the scheduler dirty and
// ensures one flush tick is in flight. The returned cmd (nil on the immediate
// path) must reach the bubbletea runtime.
func (m *Model) requestStreamRender() tea.Cmd {
	elapsed := m.now().Sub(m.sched.lastFlush)
	if elapsed >= m.sched.minInterval {
		m.rebuildViewport()
		return nil
	}
	m.sched.dirty = true
	if !m.sched.timerActive {
		m.sched.timerActive = true
		remaining := m.sched.minInterval - elapsed
		return tea.Tick(remaining, func(time.Time) tea.Msg { return frameTickMsg{} })
	}
	return nil
}

// flushStreamFrame handles a frame tick: one rebuild if anything was marked
// dirty since the last flush.
func (m *Model) flushStreamFrame() {
	m.sched.timerActive = false
	if m.sched.dirty {
		m.rebuildViewport()
	}
}
