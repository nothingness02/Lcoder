package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeComponent struct {
	id string
}

func (f fakeComponent) ID() string                            { return f.id }
func (f fakeComponent) Kind() BlockKind                       { return BlockUser }
func (f fakeComponent) Height(width int, expanded bool) int   { return 1 }
func (f fakeComponent) Render(width int, expanded bool) string { return "fake" }

func TestBlockComponentInterface(t *testing.T) {
	var comp BlockComponent = fakeComponent{id: "x"}
	if comp.ID() != "x" {
		t.Fatal("ID mismatch")
	}
}

func TestUpdatableComponentInterface(t *testing.T) {
	var comp UpdatableComponent = fakeUpdatable{fakeComponent{id: "u"}}
	if _, ok := comp.(BlockComponent); !ok {
		t.Fatal("UpdatableComponent must embed BlockComponent")
	}
}

type fakeUpdatable struct {
	fakeComponent
}

func (f fakeUpdatable) Update(msg tea.Msg) (BlockComponent, tea.Cmd) {
	return f, nil
}

func TestUpdatableComponentID(t *testing.T) {
	var comp UpdatableComponent = fakeUpdatable{fakeComponent{id: "u"}}
	if comp.ID() != "u" {
		t.Fatal("ID mismatch")
	}
}
