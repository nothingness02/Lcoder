package contextmgr

import "testing"

func TestMemoryBlocksAreSystemBlocks(t *testing.T) {
	for _, kind := range []BlockKind{BlockMemory, BlockUserProfile} {
		b := NewBlock(kind, string(kind), StabilityStable, 70)
		if !IsSystemBlock(b) {
			t.Fatalf("kind %q should be a system block", kind)
		}
	}
}

func TestDefaultBlockOrderIncludesMemory(t *testing.T) {
	order := DefaultBlockOrder()
	expected := []BlockKind{BlockSystem, BlockMode, BlockSkills, BlockProjectDocs, BlockMemory, BlockUserProfile, BlockSummary, BlockRetrieval, BlockRecent}
	if len(order) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, order)
	}
	for i, k := range expected {
		if order[i] != k {
			t.Fatalf("position %d: expected %q, got %q", i, k, order[i])
		}
	}
}
