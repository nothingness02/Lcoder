package agent

import (
	"strings"
	"testing"
)

// The turn-boundary injection runs once per step, so it must never accumulate
// into the cached prefix. An earlier version wrote the mode text into the
// system block and re-read that block as its own base, appending one extra
// copy per step and busting the cached prefix each time. The mode text now
// goes to an ephemeral reminder, so the invariants are: the system prompt is
// untouched no matter how many steps run, and each step carries at most one
// mode reminder (most steps carry none — the injector stays silent between
// refreshes).
func TestApplyModeIsIdempotentAcrossSteps(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name:         "plan",
		SystemPrompt: "MODE PROMPT",
		SparsePrompt: "MODE SPARSE",
	})

	ag.refreshEphemeralReminders(0)
	ag.applyMode()
	first := ag.mgr.SystemPrompt()
	if got := reminderText(t, ag); !strings.Contains(got, "MODE PROMPT") {
		t.Fatalf("turn 0 must carry the full mode prompt, got %q", got)
	}

	sawSparse := false
	for turn := 1; turn <= modeReminderFullRefreshTurns; turn++ {
		ag.refreshEphemeralReminders(turn)
		ag.applyMode()

		if got := ag.mgr.SystemPrompt(); got != first {
			t.Fatalf("system prompt drifted after repeated applyMode:\nfirst: %q\nafter: %q", first, got)
		}
		reminders := ag.mgr.EphemeralReminders()
		if len(reminders) > 1 {
			t.Fatalf("turn %d: expected at most one mode reminder per turn, got %v", turn, reminders)
		}
		if len(reminders) == 1 && strings.Contains(reminders[0], "MODE SPARSE") {
			sawSparse = true
		}
	}

	if strings.Contains(first, "MODE PROMPT") {
		t.Fatalf("mode text must never enter the system prompt, got %q", first)
	}
	if !sawSparse {
		t.Fatal("expected the sparse reminder to appear on at least one intermediate turn")
	}
	if got := reminderText(t, ag); !strings.Contains(got, "MODE PROMPT") {
		t.Fatalf("turn %d must refresh to the full prompt, got %q", modeReminderFullRefreshTurns, got)
	}
}
