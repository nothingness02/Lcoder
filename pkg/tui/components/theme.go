package components

import "github.com/charmbracelet/lipgloss"

// Design tokens exported for cross-package use.
var (
	ColorDim       = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	ColorSecondary = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	ColorSuccess   = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	ColorError     = lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
	ColorWarn      = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	ColorInfo      = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	ColorAccent    = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#88C0D0"}
)

func StyleDim() lipgloss.Style       { return lipgloss.NewStyle().Foreground(ColorDim) }
func StyleSecondary() lipgloss.Style { return lipgloss.NewStyle().Foreground(ColorSecondary) }
func StyleSuccess() lipgloss.Style   { return lipgloss.NewStyle().Foreground(ColorSuccess) }
func StyleError() lipgloss.Style     { return lipgloss.NewStyle().Foreground(ColorError) }
func StyleWarn() lipgloss.Style      { return lipgloss.NewStyle().Foreground(ColorWarn) }
func StyleInfo() lipgloss.Style      { return lipgloss.NewStyle().Foreground(ColorInfo) }
func StyleAccent() lipgloss.Style    { return lipgloss.NewStyle().Foreground(ColorAccent) }

var (
	colorSecondary = ColorSecondary
)

func styleDim() lipgloss.Style       { return StyleDim() }
func styleSecondary() lipgloss.Style { return StyleSecondary() }
func styleSuccess() lipgloss.Style   { return StyleSuccess() }
func styleError() lipgloss.Style     { return StyleError() }
func styleAccent() lipgloss.Style    { return StyleAccent() }
