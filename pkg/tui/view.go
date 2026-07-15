package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// rebuildViewport re-renders all blocks into the viewport and pins to bottom
// while streaming or when the user is already at the bottom.
func (m *Model) rebuildViewport() {
	atBottom := m.viewport.AtBottom()
	content := buildVirtualContent(
		m.components,
		m.viewport.Width,
		m.viewport.Height,
		m.viewport.YOffset,
		m.toolsExpanded,
	)
	m.viewport.SetContent(content)
	if m.streaming || atBottom {
		m.viewport.GotoBottom()
	}
}

// bottomHeight reports how many terminal rows the bottom region occupies. It is
// measured by rendering so it never drifts from the actual layout.
func (m *Model) bottomHeight() int {
	if m.width == 0 {
		return 3
	}
	return lipgloss.Height(m.bottomRegion())
}

// bottomRegion renders the composer, optional slash menu, suggestion, and status.
func (m *Model) bottomRegion() string {
	var sections []string

	if m.state == stateConfirm {
		sections = append(sections, m.confirm.View(m.mainWidth))
		return strings.Join(sections, "\n")
	}

	if m.menuVisible {
		matches := menuMatches(m.input.Value())
		sections = append(sections, renderMenu(matches, m.menuSelected, m.mainWidth))
	} else if m.fileMenuVisible {
		sections = append(sections, renderFileMenu(m.fileMenuItems, m.fileMenuSelected, m.mainWidth))
	} else if m.cmdPanel.visible {
		sections = append(sections, renderCmdPanel(m.cmdPanel, m.mainWidth))
	}

	sections = append(sections, m.input.View())

	if m.suggestion != "" {
		sections = append(sections, styleFaint().Render("  "+m.suggestion))
	}

	sections = append(sections, m.statusLineView())

	return strings.Join(sections, "\n")
}

// statusLineView builds the one-line status bar for the current state.
func (m *Model) statusLineView() string {
	var left string
	switch m.state {
	case stateProcessing:
		left = m.spinner.view()
		if m.compacting {
			left += styleDim().Render(" 压缩中…")
		}
	default:
		left = styleDim().Render(m.modeLabel())
	}
	return statusLine(m.mainWidth, left, m.contextRight())
}

// modeLabel returns the current agent mode for the status bar.
func (m *Model) modeLabel() string {
	if mode := m.agent.Mode(); mode != "" {
		return mode
	}
	return "ready"
}

// contextRight builds the right-aligned status segment (model + cost).
func (m *Model) contextRight() string {
	seg := m.model
	if m.totalCost > 0 {
		seg += fmtCost(m.totalCost)
	}
	return styleDim().Render(seg)
}

// View implements tea.Model.
func (m Model) View() string {
	switch m.state {
	case stateStartup:
		return m.startupView()
	case stateSessionPicker:
		return m.picker.View()
	case stateExtensions:
		return m.extPanel.View(m.width, m.height)
	case stateProvider:
		return m.renderProviderPanel()
	}

	top := m.viewport.View()
	bottom := m.bottomRegion()
	main := lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	if m.taskSidebarVisible() {
		return lipgloss.JoinHorizontal(lipgloss.Top, main, renderTaskSidebar(m.tasks, m.height))
	}
	return main
}

// startupView renders the animated logo + header over an empty body.
func (m Model) startupView() string {
	logo := logoFrame(m.headerFrame)
	hdr := renderHeader(m.header, m.headerFrame, m.width)
	hint := styleDim().Render("  Press any key to begin")
	body := lipgloss.JoinVertical(lipgloss.Center, logo, "", hdr, "", hint)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

// fmtCost formats a dollar cost segment (" · $0.0123").
func fmtCost(c float64) string {
	return fmt.Sprintf(" · $%.4f", c)
}
