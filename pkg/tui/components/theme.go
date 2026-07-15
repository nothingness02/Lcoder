package components

import "github.com/charmbracelet/lipgloss"

var (
	colorDim       = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	colorSecondary = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	colorUserBar   = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
)

func styleDim() lipgloss.Style { return lipgloss.NewStyle().Foreground(colorDim) }
