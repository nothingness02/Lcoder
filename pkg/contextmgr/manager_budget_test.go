package contextmgr

import "testing"

func TestSetBudgetReplacesInPlace(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 1000, TargetTotal: 900, ReserveOutput: 100})
	if got := m.Budget().MaxTotal; got != 1000 {
		t.Fatalf("initial MaxTotal = %d, want 1000", got)
	}

	m.SetBudget(TokenBudget{MaxTotal: 200000, TargetTotal: 180000, ReserveOutput: 8192, DropThreshold: 0.9})

	b := m.Budget()
	if b.MaxTotal != 200000 || b.TargetTotal != 180000 || b.ReserveOutput != 8192 {
		t.Fatalf("SetBudget did not replace budget, got %+v", b)
	}
	if b.DropLimit() != 172627 { // int(float64(200000-8192) * 0.9)
		t.Fatalf("DropLimit = %d, want 172627", b.DropLimit())
	}
}
