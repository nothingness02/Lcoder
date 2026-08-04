package agent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func modeReminderAgent(t *testing.T, mode ModeConfig) *Agent {
	t.Helper()
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192, MaxOutput: 16384},
	)
	mgr.SetSystemPrompt("BASE PROMPT")

	mm := NewModeManager()
	mm.modes[mode.Name] = mode

	ag, err := NewBuilder().
		WithConfig(Config{
			BaseSystemPrompt: "BASE PROMPT",
			Model:            models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
			ContextManager:   mgr,
			Mode:             mode.Name,
			ModeManager:      mm,
		}).
		WithGatewayClient(llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("ok"), nil)))).
		WithRegistry(testRegistry(t.TempDir())).
		WithPermissions(permissions.NewEngine(permissions.DefaultConfig())).
		WithEventBus(events.New()).
		Build()
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	return ag
}

// reminderText returns the single pending ephemeral reminder, failing otherwise.
func reminderText(t *testing.T, ag *Agent) string {
	t.Helper()
	got := ag.mgr.EphemeralReminders()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 ephemeral reminder, got %d: %v", len(got), got)
	}
	return got[0]
}

// modeReminderAt runs the turn-boundary injection for the given turn and
// returns the joined reminder text ("" when every injector stayed silent).
func modeReminderAt(ag *Agent, turn int) string {
	ag.refreshEphemeralReminders(turn)
	return strings.Join(ag.mgr.EphemeralReminders(), "\n\n")
}

// The mode prompt must reach the model as an ephemeral reminder and must NOT
// enter the system prompt or a context block: both sit in the provider cache
// prefix, so writing there is what a switch_mode used to invalidate.
func TestApplyMode_ReminderNotSystemPrompt(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{Name: "code", SystemPrompt: "MODE PROMPT"})
	ag.refreshEphemeralReminders(0)
	ag.applyMode()

	if got := reminderText(t, ag); !strings.Contains(got, "MODE PROMPT") {
		t.Fatalf("expected mode prompt in reminder, got %q", got)
	}
	if sp := ag.mgr.SystemPrompt(); sp != "BASE PROMPT" {
		t.Fatalf("system prompt must stay pristine, got %q", sp)
	}
	if _, ok := ag.mgr.GetBlock(contextmgr.BlockMode, "mode"); ok {
		t.Fatal("mode block must not be written: it would sit in the cache prefix")
	}
}

func TestApplyMode_NoModeSystemPrompt(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{Name: "code"})
	ag.refreshEphemeralReminders(0)
	ag.applyMode()

	if got := ag.mgr.EphemeralReminders(); len(got) != 0 {
		t.Fatalf("expected no reminder when mode has no prompt, got %v", got)
	}
	if sp := ag.mgr.SystemPrompt(); sp != "BASE PROMPT" {
		t.Fatalf("expected base prompt only, got %q", sp)
	}
}

// The tool array must be byte-identical regardless of mode restrictions —
// tools are the first layer of the cache prefix.
func TestApplyMode_ToolsUnfiltered(t *testing.T) {
	unrestricted := modeReminderAgent(t, ModeConfig{Name: "code", SystemPrompt: "M"})
	_, baseline := unrestricted.applyMode()

	restricted := modeReminderAgent(t, ModeConfig{
		Name:         "plan",
		SystemPrompt: "M",
		AllowedTools: []string{"read"},
		DeniedTools:  []string{"write", "edit", "bash"},
	})
	_, got := restricted.applyMode()

	if len(got) != len(baseline) {
		t.Fatalf("mode restrictions must not filter the tool array: got %d tools, want %d",
			len(got), len(baseline))
	}
	for i := range got {
		if got[i].Name != baseline[i].Name {
			t.Fatalf("tool %d: got %q, want %q", i, got[i].Name, baseline[i].Name)
		}
	}
}

// The mode reminder runs on a quiet cadence: the full prompt on entry,
// silence on the following turn, the abbreviated form once at least
// modeReminderSparseTurns turns have passed, and a full refresh after
// modeReminderFullRefreshTurns turns. Most turns inject nothing.
func TestModeReminder_FullThenSilentSparseThenRefresh(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name:         "plan",
		SystemPrompt: "FULL TEXT",
		SparsePrompt: "SPARSE TEXT",
	})

	if got := modeReminderAt(ag, 0); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn 0 must be full, got %q", got)
	}
	if got := modeReminderAt(ag, 1); got != "" {
		t.Fatalf("turn 1 must be silent, got %q", got)
	}
	for _, turn := range []int{2, 4} {
		got := modeReminderAt(ag, turn)
		if !strings.Contains(got, "SPARSE TEXT") {
			t.Fatalf("turn %d must be sparse, got %q", turn, got)
		}
		if strings.Contains(got, "FULL TEXT") {
			t.Fatalf("turn %d must not repeat the full text, got %q", turn, got)
		}
	}
	if got := modeReminderAt(ag, 3); got != "" {
		t.Fatalf("turn 3 must be silent, got %q", got)
	}
	if got := modeReminderAt(ag, modeReminderFullRefreshTurns); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn %d must refresh to full, got %q", modeReminderFullRefreshTurns, got)
	}
}

// Without a sparse_prompt the sparse tier stays silent instead of falling
// back to the full text: between full refreshes the turn injects nothing.
func TestModeReminder_NoSparseStaysSilent(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{Name: "plan", SystemPrompt: "FULL TEXT"})

	if got := modeReminderAt(ag, 0); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn 0 must be full, got %q", got)
	}
	for turn := 1; turn < modeReminderFullRefreshTurns; turn++ {
		if got := modeReminderAt(ag, turn); got != "" {
			t.Fatalf("turn %d must be silent without a sparse prompt, got %q", turn, got)
		}
	}
	if got := modeReminderAt(ag, modeReminderFullRefreshTurns); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn %d must refresh to full, got %q", modeReminderFullRefreshTurns, got)
	}
}

// Switching modes forces a full prompt and announces that the old mode's
// restrictions are lifted. The notice is a one-shot event: it is persisted
// into the conversation history rather than riding the ephemeral channel, so
// the model can find the transition point when it looks back.
func TestModeReminder_SwitchAnnouncesRelease(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name: "plan", SystemPrompt: "PLAN TEXT", SparsePrompt: "PLAN SPARSE",
	})
	ag.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "CODE TEXT"}

	modeReminderAt(ag, 0)
	modeReminderAt(ag, 1) // silent; would be sparse-eligible by turn 2

	ag.cfg.Mode = "code"
	got := modeReminderAt(ag, 2)
	if strings.Contains(got, "switched from") {
		t.Fatalf("release notice must not ride the ephemeral channel, got %q", got)
	}
	if !strings.Contains(got, "CODE TEXT") {
		t.Fatalf("switch must force the full prompt, got %q", got)
	}
	if n := countHistoryNotices(ag); n != 1 {
		t.Fatalf("expected exactly one persisted release notice, got %d", n)
	}

	modeReminderAt(ag, 3)
	if n := countHistoryNotices(ag); n != 1 {
		t.Fatalf("release notice must be one-shot, got %d persisted notices", n)
	}
}

// countHistoryNotices counts persisted mode-switch release notices in the
// conversation history.
func countHistoryNotices(ag *Agent) int {
	n := 0
	for _, msg := range ag.mgr.AllMessages() {
		if strings.Contains(msg.Text(), "switched from") {
			n++
		}
	}
	return n
}

// Both mode-switch paths must produce the same context. WithMode (the /mode
// command) builds a fresh Agent, so without carrying the reminder bookkeeping it
// sees no previous mode and skips the release notice that switch_mode emits —
// the same user action leaving the model in two different states.
func TestWithModeEmitsReleaseNoticeLikeSwitchMode(t *testing.T) {
	newAgent := func() *Agent {
		ag := modeReminderAgent(t, ModeConfig{
			Name: "plan", SystemPrompt: "PLAN", SparsePrompt: "PLAN SPARSE",
		})
		ag.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "CODE"}
		ag.refreshEphemeralReminders(0) // establish plan mode as the previous reminder
		return ag
	}

	// switch_mode path: the executor mutates cfg.Mode in place.
	viaTool := newAgent()
	viaTool.cfg.Mode = "code"
	viaTool.refreshEphemeralReminders(1)
	toolText := reminderText(t, viaTool)

	// WithMode path: a fresh Agent continuing the same conversation.
	fresh := newAgent().WithMode("code").(*Agent)
	fresh.refreshEphemeralReminders(1)
	withModeText := reminderText(t, fresh)

	for _, tc := range []struct {
		name string
		ag   *Agent
		got  string
	}{
		{"switch_mode", viaTool, toolText},
		{"WithMode", fresh, withModeText},
	} {
		if strings.Contains(tc.got, "switched from") {
			t.Errorf("%s: release notice must not ride the ephemeral channel, got %q", tc.name, tc.got)
		}
		if !strings.Contains(tc.got, "CODE") {
			t.Errorf("%s: switch must force the full prompt, got %q", tc.name, tc.got)
		}
		if n := countHistoryNotices(tc.ag); n != 1 {
			t.Errorf("%s: expected exactly one persisted release notice, got %d", tc.name, n)
		}
	}
}
