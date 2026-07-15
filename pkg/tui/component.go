package tui

import tea "github.com/charmbracelet/bubbletea"

// BlockKind is the type of a conversation block. Exported as a named type so
// external renderers (e.g. a web renderer) can identify block categories.
type BlockKind int

const (
	BlockUser BlockKind = iota
	BlockAssistant
	BlockTool
	BlockSystem
)

// BlockComponent is a renderable unit inside the conversation view.
type BlockComponent interface {
	ID() string
	Kind() BlockKind
	Height(width int, expanded bool) int
	Render(width int, expanded bool) string
}

// UpdatableComponent is used for blocks that need local interaction.
type UpdatableComponent interface {
	BlockComponent
	Update(msg tea.Msg) (BlockComponent, tea.Cmd)
}

// ComponentMsg routes a tea.Msg to the component with the given ID.
type ComponentMsg struct {
	ID  string
	Msg tea.Msg
}

// ToggleExpandedMsg requests toggling a component's expanded/collapsed state.
type ToggleExpandedMsg struct{}
