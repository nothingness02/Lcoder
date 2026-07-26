package contextmgr

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestBuildTurnRequestCarriesThinking(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 100000, ReserveOutput: 1000}, WithThinking("low"))
	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "p", ID: "m"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	if req.Thinking != "low" {
		t.Errorf("Thinking = %q, want low", req.Thinking)
	}
	m2 := NewManager(TokenBudget{MaxTotal: 100000, ReserveOutput: 1000})
	req2, err := m2.BuildTurnRequest(models.ModelRef{Provider: "p", ID: "m"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	if req2.Thinking != "" {
		t.Errorf("default Thinking must be empty, got %q", req2.Thinking)
	}
}
