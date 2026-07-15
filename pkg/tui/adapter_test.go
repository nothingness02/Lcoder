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

func TestFallbackComponentID(t *testing.T) {
	b := block{id: "b1", kind: BlockUser, raw: "hello"}
	comp := toComponent(b)
	if comp.ID() != "b1" {
		t.Fatalf("ID = %q, want b1", comp.ID())
	}
}

func TestFallbackComponentRender(t *testing.T) {
	b := block{kind: BlockSystem, raw: "ready"}
	comp := toComponent(b)
	out := comp.Render(40, false)
	if out == "" {
		t.Fatal("expected non-empty render")
	}
}
