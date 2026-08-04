package contextmgr

import (
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// Ephemeral system-reminders are short, per-turn instructions injected into the
// built request for the CURRENT turn only. They are never written into any
// block, so they disappear on the next turn unless re-set — exactly the
// "ephemeral" lifecycle Claude Code uses for its <system-reminder> blocks (todo
// nudges, malformed-output warnings, context-bloat hints, etc.). Keeping them
// out of persisted history also keeps the cached prefix stable across turns.

// AddEphemeralReminder appends one reminder for the next BuildTurnRequest.
func (m *Manager) AddEphemeralReminder(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ephemeralReminders = append(m.ephemeralReminders, text)
}

// SetEphemeralReminders replaces the pending reminders.
func (m *Manager) SetEphemeralReminders(reminders []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ephemeralReminders = append([]string(nil), reminders...)
}

// ClearEphemeralReminders drops all pending reminders. The agent loop calls this
// at each turn boundary so a reminder set for one turn never bleeds into the
// next.
func (m *Manager) ClearEphemeralReminders() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ephemeralReminders = nil
}

// EphemeralReminders returns a copy of the pending reminders.
func (m *Manager) EphemeralReminders() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ephemeralReminders...)
}

// wrapReminder wraps text in a <system-reminder> envelope, matching the on-wire
// shape an Anthropic-style model is trained to treat as harness-injected context
// rather than user instruction.
func wrapReminder(text string) string {
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

// buildEphemeralMessage returns the synthetic trailing user message carrying all
// pending reminders, or ok=false when none are set. The message is tagged
// metadata ephemeral=true so callers (and snapshots) can distinguish it from
// real conversation, and so BuildTurnRequest excludes it from cache breakpoints.
func (m *Manager) buildEphemeralMessage() (models.AgentMessage, bool) {
	if len(m.ephemeralReminders) == 0 {
		return models.AgentMessage{}, false
	}
	parts := make([]string, 0, len(m.ephemeralReminders))
	for _, r := range m.ephemeralReminders {
		parts = append(parts, wrapReminder(r))
	}
	msg := models.NewAgentMessage(models.RoleUser, models.TextContent{
		Text: strings.Join(parts, "\n\n"),
	}).WithMetadata("ephemeral", true)
	return msg, true
}

// IsEphemeral reports whether a message is an injected ephemeral reminder.
func IsEphemeral(msg models.AgentMessage) bool {
	if msg.Metadata == nil {
		return false
	}
	v, ok := msg.Metadata["ephemeral"].(bool)
	return ok && v
}
