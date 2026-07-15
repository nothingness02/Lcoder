package components

import "github.com/charmbracelet/lipgloss"

var (
	colorDim       = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	colorSecondary = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	colorUserBar   = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
	colorSuccess   = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	colorError     = lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
	colorAccent    = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#88C0D0"}
)

func styleDim() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorDim) }
func styleSuccess() lipgloss.Style { return lipgloss.NewStyle().Foreground(colorSuccess) }
func styleError() lipgloss.Style   { return lipgloss.NewStyle().Foreground(colorError) }
func styleAccent() lipgloss.Style  { return lipgloss.NewStyle().Foreground(colorAccent) }
