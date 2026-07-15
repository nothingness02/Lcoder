package components

import "github.com/charmbracelet/lipgloss"

// Design tokens exported for cross-package use.
var (
	ColorDim       = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	ColorSecondary = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	ColorUserBar   = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
	ColorSuccess   = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	ColorError     = lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
	ColorAccent    = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#88C0D0"}
)

func StyleDim() lipgloss.Style       { return lipgloss.NewStyle().Foreground(ColorDim) }
func StyleSecondary() lipgloss.Style { return lipgloss.NewStyle().Foreground(ColorSecondary) }
func StyleSuccess() lipgloss.Style   { return lipgloss.NewStyle().Foreground(ColorSuccess) }
func StyleError() lipgloss.Style     { return lipgloss.NewStyle().Foreground(ColorError) }
func StyleAccent() lipgloss.Style    { return lipgloss.NewStyle().Foreground(ColorAccent) }

var (
	colorSecondary = ColorSecondary
	colorUserBar   = ColorUserBar
)

func styleDim() lipgloss.Style     { return StyleDim() }
func styleSuccess() lipgloss.Style { return StyleSuccess() }
func styleError() lipgloss.Style   { return StyleError() }
func styleAccent() lipgloss.Style  { return StyleAccent() }
