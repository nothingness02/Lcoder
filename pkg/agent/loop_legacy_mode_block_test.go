package agent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

// A checkpoint written before mode text moved to an ephemeral reminder restores
// a BlockMode block. Nothing writes that kind anymore, so without an explicit
// eviction it would sit in the system prompt — and therefore the provider cache
// prefix — for the rest of the session. Asserted with and without a configured
// mode manager, because the eviction has to run on both paths.
func TestApplyMode_EvictsLegacyModeBlock(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clearModeMgr bool
	}{
		{name: "with mode manager"},
		{name: "without mode manager", clearModeMgr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := modeReminderAgent(t, ModeConfig{Name: "code", SystemPrompt: "MODE PROMPT"})
			if tc.clearModeMgr {
				ag.cfg.ModeManager = nil
			}

			ag.mgr.SetBlock(contextmgr.NewBlock(
				contextmgr.BlockMode, "mode", contextmgr.StabilityStable, 90,
				models.NewAgentMessage(models.RoleSystem,
					models.TextContent{Text: "STALE MODE BLOCK"}),
			))

			ag.applyMode()

			if _, ok := ag.mgr.GetBlock(contextmgr.BlockMode, "mode"); ok {
				t.Fatal("legacy mode block survived applyMode")
			}
			if sp := ag.mgr.SystemPrompt(); strings.Contains(sp, "STALE MODE BLOCK") {
				t.Fatalf("stale mode text still in the cache prefix: %q", sp)
			}
		})
	}
}
