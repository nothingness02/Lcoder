package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// cmdPanelKind distinguishes a read-only text box from an interactive list.
type cmdPanelKind int

const (
	cmdPanelText cmdPanelKind = iota
	cmdPanelSelect
)

// cmdPanelAction is the side effect to run when a select row is chosen.
type cmdPanelAction int

const (
	actionNone cmdPanelAction = iota
	actionSwitchMode
	actionTriggerSkill
	actionMCPManage
	actionApplyAccent
	actionToggleSkill
)

// cmdPanelItem is one selectable row (mode or skill).
type cmdPanelItem struct {
	label string
	desc  string
	value string
}

// cmdPanel is the ephemeral panel shown above the composer for command output.
// It replaces routing command feedback into the main viewport: the viewport now
// holds only agent-run content. A text panel displays /help and /status; a
// select panel drives /modes, /mode, and /skill.
type cmdPanel struct {
	visible  bool
	kind     cmdPanelKind
	title    string
	text     string
	items    []cmdPanelItem
	selected int
	action   cmdPanelAction
}

// moveUp/moveDown clamp the selection within the item range.
func (p *cmdPanel) moveUp() {
	if p.selected > 0 {
		p.selected--
	}
}

func (p *cmdPanel) moveDown() {
	if p.selected < len(p.items)-1 {
		p.selected++
	}
}

// renderCmdPanel draws the panel in a rounded box, matching the slash menu.
func renderCmdPanel(p cmdPanel, width int) string {
	if !p.visible {
		return ""
	}
	inner := max(width-4, 1) // border (2) + padding (2)
	var lines []string
	if p.title != "" {
		lines = append(lines, styleDim().Render("/"+p.title))
	}
	switch p.kind {
	case cmdPanelSelect:
		for i, it := range p.items {
			row := it.label
			if it.desc != "" {
				row += styleDim().Render("  " + it.desc)
			}
			if i == p.selected {
				row = lipgloss.NewStyle().Foreground(colorSelect).Render("› ") + row
			} else {
				row = "  " + row
			}
			lines = append(lines, truncateCells(row, inner, "…"))
		}
	default: // cmdPanelText
		for _, ln := range strings.Split(p.text, "\n") {
			lines = append(lines, truncateCells(ln, inner, "…"))
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint)
	return box.Render(strings.Join(lines, "\n"))
}
