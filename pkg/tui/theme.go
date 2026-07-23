package tui

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/tui/components"
	"github.com/lcoder/lcoder/pkg/tui/markdown"
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
// The result is also pushed into the markdown package so its glamour renderers
// pick the matching palette (the markdown package cannot probe the terminal
// itself once bubbletea owns stdin).
func warmBackgroundColor() {
	dark := isDarkBackground()
	markdown.SetDarkBackground(dark)
}

// Semantic palette. Every color is an AdaptiveColor so the TUI stays readable on
// both dark and light terminals. Light = value shown on a light background.
var (
	colorDim        = components.ColorDim
	colorFaint      = lipgloss.AdaptiveColor{Light: "252", Dark: "237"}
	colorError      = components.ColorError
	colorWarn       = components.ColorWarn
	colorInfo       = components.ColorInfo
	colorAccent     = components.ColorAccent
	colorSelect     = lipgloss.AdaptiveColor{Light: "25", Dark: "111"}
	colorSelectDesc = lipgloss.AdaptiveColor{Light: "242", Dark: "146"}
)

// accentPreset parametrizes a swap of the accent token.
type accentPreset struct {
	name        string
	desc        string
	dark, light string
}

// accentPresets are the selectable accent color themes for /color. Lcoder's
// Nord cyan is the default; the rest mirror Kocoro's preset names. Only the
// accent token swaps (header border/title, status bar, pickers, tool markers);
// already-committed scrollback keeps its original color. Not persisted - resets
// on restart, matching Kocoro.
var accentPresets = []accentPreset{
	{"lcoder", "Nord cyan (default)", "#88C0D0", "#5E81AC"},
	{"kocoro", "brand pink", "#FF5C8A", "#C9105A"},
	{"ocean", "calm blue", "#5CA8FF", "#1060C9"},
	{"forest", "green", "#5CCB6E", "#1A8A3A"},
	{"violet", "purple", "#B98CFF", "#6A30C9"},
}

func applyAccent(p accentPreset) {
	components.ColorAccent = lipgloss.AdaptiveColor{Light: p.light, Dark: p.dark}
	colorAccent = components.ColorAccent
}

func styleDim() lipgloss.Style       { return components.StyleDim() }
func styleFaint() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorFaint) }
func styleSuccess() lipgloss.Style   { return components.StyleSuccess() }
func styleError() lipgloss.Style     { return components.StyleError() }
func styleWarn() lipgloss.Style      { return components.StyleWarn() }
func styleInfo() lipgloss.Style      { return components.StyleInfo() }
func styleAccent() lipgloss.Style    { return components.StyleAccent() }
