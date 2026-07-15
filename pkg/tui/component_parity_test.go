package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestComponentParity_System(t *testing.T) {
	for _, width := range []int{20, 40, 80} {
		b := block{kind: components.BlockSystem, raw: "model loaded"}
		comp := toComponent(b)
		got := comp.Render(width, false)
		want := b.render(width, false)
		if got != want {
			t.Fatalf("width=%d: component render differs from legacy render\ngot:\n%q\nwant:\n%q", width, got, want)
		}
	}
}

func TestComponentParity_User(t *testing.T) {
	for _, width := range []int{20, 40, 80} {
		b := block{
			kind:        components.BlockUser,
			raw:         "hello world",
			attachments: []string{"main.go", "README.md"},
		}
		comp := toComponent(b)
		got := comp.Render(width, false)
		want := b.render(width, false)
		if got != want {
			t.Fatalf("width=%d: component render differs from legacy render\ngot:\n%q\nwant:\n%q", width, got, want)
		}
	}
}
