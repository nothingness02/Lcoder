package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)
func TestComponentsFromBlocks(t *testing.T) {
	blocks := []block{
		{kind: components.BlockSystem, raw: "ready"},
		{kind: components.BlockUser, raw: "hi"},
	}
	comps := componentsFromBlocks(blocks)
	if len(comps) != 2 {
		t.Fatalf("expected 2 components, got %d", len(comps))
	}
	if comps[0].Kind() != components.BlockSystem {
		t.Fatalf("first kind = %v, want components.BlockSystem", comps[0].Kind())
	}
	if comps[1].Kind() != components.BlockUser {
		t.Fatalf("second kind = %v, want components.BlockUser", comps[1].Kind())
	}
}

func TestFallbackComponentID(t *testing.T) {
	b := block{id: "b1", kind: components.BlockUser, raw: "hello"}
	comp := toComponent(b)
	if comp.ID() != "b1" {
		t.Fatalf("ID = %q, want b1", comp.ID())
	}
}

func TestSystemComponentRender(t *testing.T) {
	b := block{kind: components.BlockSystem, raw: "ready"}
	comp := toComponent(b)
	out := comp.Render(40, false)
	if out == "" {
		t.Fatal("expected non-empty render")
	}
}
