package tui

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

var (
	darkBgOnce sync.Once
	darkBg     = true
)

// isDarkBackground reports whether the terminal has a dark background, detected
// once via lipgloss and cached. warmBackgroundColor MUST run before bubbletea
// grabs stdin (in Run/RunWithIO before tea.NewProgram), else the OSC 11 reply is
// swallowed and detection silently falls back to dark.
func isDarkBackground() bool {
	darkBgOnce.Do(func() {
		darkBg = lipgloss.HasDarkBackground()
	})
	return darkBg
}

// warmBackgroundColor forces background detection now, while stdin is still free.
func warmBackgroundColor() { _ = isDarkBackground() }

// Semantic palette. Every color is an AdaptiveColor so the TUI stays readable on
// both dark and light terminals. Light = value shown on a light background.
var (
	colorDim        = components.ColorDim
	colorSecondary  = components.ColorSecondary
	colorFaint      = lipgloss.AdaptiveColor{Light: "252", Dark: "237"}
	colorError      = components.ColorError
	colorAccent     = components.ColorAccent
	colorSelect     = lipgloss.AdaptiveColor{Light: "25", Dark: "111"}
	colorSelectDesc = lipgloss.AdaptiveColor{Light: "242", Dark: "146"}
	colorUserBar    = components.ColorUserBar
)

// accentPreset parametrizes a swap of the accent token.
type accentPreset struct {
	name        string
	dark, light string
}

func applyAccent(p accentPreset) {
	components.ColorAccent = lipgloss.AdaptiveColor{Light: p.light, Dark: p.dark}
	colorAccent = components.ColorAccent
}

func styleDim() lipgloss.Style       { return components.StyleDim() }
func styleSecondary() lipgloss.Style { return components.StyleSecondary() }
func styleFaint() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorFaint) }
func styleSuccess() lipgloss.Style   { return components.StyleSuccess() }
func styleError() lipgloss.Style     { return components.StyleError() }
func styleAccent() lipgloss.Style    { return components.StyleAccent() }
