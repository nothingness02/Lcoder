package agent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestSwitchModelUpdatesModelAndBudget(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 1000, TargetTotal: 900, ReserveOutput: 100})
	a := &Agent{
		cfg: Config{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"}},
		mgr: mgr,
	}

	a.SwitchModel(
		models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"},
		contextmgr.TokenBudget{MaxTotal: 200000, TargetTotal: 180000, ReserveOutput: 8192},
	)

	if a.cfg.Model.Provider != "anthropic" || a.cfg.Model.ID != "claude-sonnet-4" {
		t.Fatalf("model not switched, got %+v", a.cfg.Model)
	}
	if a.mgr.Budget().MaxTotal != 200000 {
		t.Fatalf("budget not updated, got %d", a.mgr.Budget().MaxTotal)
	}
}

func TestSwitchModelNilManager(t *testing.T) {
	a := &Agent{cfg: Config{Model: models.ModelRef{Provider: "openai", ID: "x"}}}
	// Must not panic when the manager is nil (TransformContext path).
	a.SwitchModel(models.ModelRef{Provider: "deepseek", ID: "deepseek-chat"}, contextmgr.TokenBudget{MaxTotal: 64000})
	if a.cfg.Model.Provider != "deepseek" {
		t.Fatalf("model not switched, got %+v", a.cfg.Model)
	}
}
