package agent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// Mode tool restrictions are enforced at dispatch, not by filtering the tool
// schemas, so these assertions are on modeDenies rather than on the tool array.
func TestModeDenies(t *testing.T) {
	mm := NewModeManager()
	mm.modes["plan"] = ModeConfig{
		Name:         "plan",
		AllowedTools: []string{"read", "grep", switchModeToolName},
		DeniedTools:  []string{"write", "edit", "bash"},
	}
	mm.modes["code"] = ModeConfig{Name: "code"}

	cfg := &Config{Mode: "plan", ModeManager: mm}
	e := &executor{cfg: cfg}

	for _, name := range []string{"write", "edit", "bash"} {
		reason, denied := e.modeDenies(name)
		if !denied {
			t.Fatalf("%s must be denied in plan mode", name)
		}
		if !strings.Contains(reason, switchModeToolName) {
			t.Fatalf("reason must name the escape hatch, got %q", reason)
		}
	}

	for _, name := range []string{"read", "grep", switchModeToolName} {
		if _, denied := e.modeDenies(name); denied {
			t.Fatalf("%s must be allowed in plan mode", name)
		}
	}

	// A tool absent from a non-empty allowlist is denied even without being
	// named in denied_tools.
	if _, denied := e.modeDenies("ls"); !denied {
		t.Fatal("tool outside a non-empty allowlist must be denied")
	}

	// An unrestricted mode denies nothing.
	cfg.Mode = "code"
	for _, name := range []string{"write", "edit", "bash", "ls"} {
		if _, denied := e.modeDenies(name); denied {
			t.Fatalf("%s must be allowed in unrestricted mode", name)
		}
	}
}

// The cache-invalidation scenario the redesign targets: one agent switching
// between a restricted and an unrestricted mode must send a byte-identical tool
// array both times. Tools are the first layer of the provider cache prefix, so
// any difference here re-bills the whole conversation on the next turn.
func TestSwitchMode_ToolArrayUnchanged(t *testing.T) {
	ag := modeReminderAgent(t, ModeConfig{
		Name:         "plan",
		SystemPrompt: "PLAN",
		AllowedTools: []string{"read"},
		DeniedTools:  []string{"write", "edit", "bash"},
	})
	ag.cfg.ModeManager.modes["code"] = ModeConfig{Name: "code", SystemPrompt: "CODE"}

	_, before := ag.applyMode()
	names := func(defs []models.ToolDefinition) string {
		out := make([]string, len(defs))
		for i, d := range defs {
			out[i] = d.Name
		}
		return strings.Join(out, ",")
	}
	beforeNames := names(before)

	ag.cfg.Mode = "code"
	ag.mgr.ClearEphemeralReminders()
	_, after := ag.applyMode()

	if got := names(after); got != beforeNames {
		t.Fatalf("tool array changed across switch_mode:\nbefore: %s\nafter:  %s", beforeNames, got)
	}
}

func TestModeDenies_NoModeManager(t *testing.T) {
	e := &executor{cfg: &Config{Mode: "plan"}}
	if _, denied := e.modeDenies("write"); denied {
		t.Fatal("must not deny when no mode manager is configured")
	}
}
