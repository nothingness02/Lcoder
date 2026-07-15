package components

import (
	"strings"
	"testing"
)

func TestSystemLogComponentRender(t *testing.T) {
	comp := NewSystemLogComponent("s1", "model loaded")
	out := comp.Render(40, false)
	if !strings.Contains(out, "model loaded") {
		t.Fatalf("missing text: %q", out)
	}
}

func TestSystemLogComponentHeight(t *testing.T) {
	comp := NewSystemLogComponent("s1", "one\ntwo")
	if h := comp.Height(40, false); h != 2 {
		t.Fatalf("height = %d, want 2", h)
	}
}
