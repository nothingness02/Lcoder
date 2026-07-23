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

// Caller-applied severity styling (already ANSI) must pass through unchanged so
// an error/warn line keeps its color instead of being flattened to dim italic.
func TestSystemLogComponentPassesThroughStyled(t *testing.T) {
	styled := "\x1b[31mboom\x1b[0m"
	comp := NewSystemLogComponent("s1", styled)
	if out := comp.Render(40, false); out != styled {
		t.Fatalf("styled input should pass through unchanged, got %q", out)
	}
}

// Unstyled text (e.g. rebuilt from a reloaded session) falls back to the info
// baseline; only the content is asserted so the test is color-profile agnostic.
func TestSystemLogComponentPlainFallsBack(t *testing.T) {
	comp := NewSystemLogComponent("s1", "plain note")
	if out := comp.Render(40, false); !strings.Contains(out, "plain note") {
		t.Fatalf("plain text should still render its content, got %q", out)
	}
}
