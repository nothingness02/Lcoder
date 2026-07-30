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
			BaseSystemPrompt:  "BASE PROMPT",
			Model:             models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
			ContextManager:    mgr,
			Mode:              mode.Name,
			ModeManager:       mm,
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

// The mode prompt must reach the model as an ephemeral reminder and must NOT
// enter the system prompt or a context block: both sit in the provider cache
// prefix, so writing there is what a switch_mode used to invalidate.
func TestApplyMode_ReminderNotSystemPrompt(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{Name: "code", SystemPrompt: "MODE PROMPT"})
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

// First turn sends the full prompt; the next few send the abbreviated form;
// after modeReminderFullRefreshTurns the full prompt returns.
func TestModeReminder_FullThenSparseThenRefresh(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name:         "plan",
		SystemPrompt: "FULL TEXT",
		SparsePrompt: "SPARSE TEXT",
	})

	if got := ag.modeReminder(); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn 1 must be full, got %q", got)
	}
	for i := 2; i <= modeReminderFullRefreshTurns; i++ {
		got := ag.modeReminder()
		if !strings.Contains(got, "SPARSE TEXT") {
			t.Fatalf("turn %d must be sparse, got %q", i, got)
		}
		if strings.Contains(got, "FULL TEXT") {
			t.Fatalf("turn %d must not repeat the full text, got %q", i, got)
		}
	}
	if got := ag.modeReminder(); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("turn %d must refresh to full, got %q", modeReminderFullRefreshTurns+1, got)
	}
}

// Without a sparse_prompt the full text is the only option, so it repeats.
func TestModeReminder_NoSparseFallsBackToFull(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{Name: "plan", SystemPrompt: "FULL TEXT"})
	ag.modeReminder()
	if got := ag.modeReminder(); !strings.Contains(got, "FULL TEXT") {
		t.Fatalf("expected full text fallback, got %q", got)
	}
}

// Switching modes forces a full prompt and announces that the old mode's
// restrictions are lifted — the previous reminder is gone from context, so the
// model would otherwise still be acting under it.
func TestModeReminder_SwitchAnnouncesRelease(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name: "plan", SystemPrompt: "PLAN TEXT", SparsePrompt: "PLAN SPARSE",
	})
	ag.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "CODE TEXT"}

	ag.modeReminder()
	ag.modeReminder() // would be sparse if the mode had not changed

	ag.cfg.Mode = "code"
	got := ag.modeReminder()
	if !strings.Contains(got, "switched from plan mode to code mode") {
		t.Fatalf("expected release notice naming both modes, got %q", got)
	}
	if !strings.Contains(got, "CODE TEXT") {
		t.Fatalf("switch must force the full prompt, got %q", got)
	}

	if next := ag.modeReminder(); strings.Contains(next, "switched from") {
		t.Fatalf("release notice must be one-shot, got %q", next)
	}
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
		ag.applyMode() // establish plan mode as the previous reminder
		return ag
	}

	// switch_mode path: the executor mutates cfg.Mode in place.
	viaTool := newAgent()
	viaTool.cfg.Mode = "code"
	viaTool.mgr.ClearEphemeralReminders()
	viaTool.applyMode()
	toolText := reminderText(t, viaTool)

	// WithMode path: a fresh Agent continuing the same conversation.
	fresh := newAgent().WithMode("code").(*Agent)
	fresh.mgr.ClearEphemeralReminders()
	fresh.applyMode()
	withModeText := reminderText(t, fresh)

	for _, tc := range []struct{ name, got string }{
		{"switch_mode", toolText},
		{"WithMode", withModeText},
	} {
		if !strings.Contains(tc.got, "switched from plan mode to code mode") {
			t.Errorf("%s: missing release notice, got %q", tc.name, tc.got)
		}
		if !strings.Contains(tc.got, "CODE") {
			t.Errorf("%s: switch must force the full prompt, got %q", tc.name, tc.got)
		}
	}
}
