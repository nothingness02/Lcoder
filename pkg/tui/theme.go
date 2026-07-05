package tui

import (
	"sync"

	"github.com/charmbracelet/lipgloss"
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
	colorDim       = lipgloss.AdaptiveColor{Light: "240", Dark: "245"}
	colorSecondary = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	colorFaint     = lipgloss.AdaptiveColor{Light: "252", Dark: "237"}
	colorSuccess   = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	colorError     = lipgloss.AdaptiveColor{Light: "160", Dark: "196"}
	// colorAccent — Lcoder frost cyan (Nord). Dark #88C0D0, Light #5E81AC.
	colorAccent     = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#88C0D0"}
	colorSelect     = lipgloss.AdaptiveColor{Light: "25", Dark: "111"}
	colorSelectDesc = lipgloss.AdaptiveColor{Light: "242", Dark: "146"}
	// colorUserBar — subtle background tint for the full-width user bar.
	colorUserBar = lipgloss.AdaptiveColor{Light: "254", Dark: "237"}
)

// accentPreset is a selectable accent for /color. Only colorAccent is swapped.
type accentPreset struct {
	name        string
	desc        string
	dark, light string
}

var accentPresets = []accentPreset{
	{"frost", "cyan (default)", "#88C0D0", "#5E81AC"},
	{"ocean", "calm blue", "#5CA8FF", "#1060C9"},
	{"aurora", "green", "#A3BE8C", "#1A8A3A"},
	{"sunset", "warm orange", "#FF9C5C", "#C95A10"},
	{"violet", "purple", "#B98CFF", "#6A30C9"},
}

func applyAccent(p accentPreset) {
	colorAccent = lipgloss.AdaptiveColor{Light: p.light, Dark: p.dark}
}

func styleDim() lipgloss.Style       { return lipgloss.NewStyle().Foreground(colorDim) }
func styleSecondary() lipgloss.Style { return lipgloss.NewStyle().Foreground(colorSecondary) }
func styleFaint() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorFaint) }
func styleSuccess() lipgloss.Style   { return lipgloss.NewStyle().Foreground(colorSuccess) }
func styleError() lipgloss.Style     { return lipgloss.NewStyle().Foreground(colorError) }
func styleAccent() lipgloss.Style    { return lipgloss.NewStyle().Foreground(colorAccent) }
