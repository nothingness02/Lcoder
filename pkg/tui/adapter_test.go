package tui

import "testing"

func TestComponentsFromBlocks(t *testing.T) {
	blocks := []block{
		{kind: BlockSystem, raw: "ready"},
		{kind: BlockUser, raw: "hi"},
	}
	comps := componentsFromBlocks(blocks)
	if len(comps) != 2 {
		t.Fatalf("expected 2 components, got %d", len(comps))
	}
	if comps[0].Kind() != BlockSystem {
		t.Fatalf("first kind = %v, want BlockSystem", comps[0].Kind())
	}
	if comps[1].Kind() != BlockUser {
		t.Fatalf("second kind = %v, want BlockUser", comps[1].Kind())
	}
}
