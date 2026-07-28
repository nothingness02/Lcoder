package agent

import (
	"strings"
	"testing"
)

// applyMode runs once per step, so it must never accumulate into the cached
// prefix. An earlier version wrote the mode text into the system block and
// re-read that block as its own base, appending one extra copy per step and
// busting the cached prefix each time. The mode text now goes to an ephemeral
// reminder, so the invariant is that the system prompt is untouched no matter
// how many times applyMode runs.
func TestApplyModeIsIdempotentAcrossSteps(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name:         "plan",
		SystemPrompt: "MODE PROMPT",
		SparsePrompt: "MODE SPARSE",
	})

	ag.applyMode()
	first := ag.mgr.SystemPrompt()
	for i := 0; i < 3; i++ {
		ag.mgr.ClearEphemeralReminders()
		ag.applyMode()
	}

	if got := ag.mgr.SystemPrompt(); got != first {
		t.Fatalf("system prompt drifted after repeated applyMode:\nfirst: %q\nafter: %q", first, got)
	}
	if strings.Contains(first, "MODE PROMPT") {
		t.Fatalf("mode text must never enter the system prompt, got %q", first)
	}
	if got := reminderText(t, ag); !strings.Contains(got, "MODE") {
		t.Fatalf("expected exactly one mode reminder per turn, got %q", got)
	}
}
