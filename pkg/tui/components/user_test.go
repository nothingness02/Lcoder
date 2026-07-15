package components

import (
	"strings"
	"testing"
)

func TestUserComponentRender(t *testing.T) {
	comp := NewUserComponent("u1", "hello", []string{"main.go"})
	out := comp.Render(40, false)
	if !strings.Contains(out, "hello") {
		t.Fatalf("missing text: %q", out)
	}
	if !strings.Contains(out, "main.go") {
		t.Fatalf("missing attachment: %q", out)
	}
}
