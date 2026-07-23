package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// rebuildViewport re-renders all blocks into the viewport and pins to bottom
// while streaming or when the user is already at the bottom. When a block is
// focused, the viewport is scrolled to keep that block visible.
func (m *Model) rebuildViewport() {
	layouts := layoutComponents(m.components, m.viewport.Width, m.toolsExpanded, m.focusedBlockIndex)
	if m.focusedBlockIndex >= 0 && m.focusedBlockIndex < len(layouts) {
		focused := layouts[m.focusedBlockIndex]
		m.viewport.SetYOffset(clamp(focused.offset, 0, maxTotalHeight(layouts)-m.viewport.Height))
	}
	atBottom := m.viewport.AtBottom()
	content := buildVirtualContent(layouts, m.viewport.Height, m.viewport.YOffset, m.toolsExpanded, m.focusedBlockIndex)
	m.viewport.SetContent(content)
	if m.focusedBlockIndex < 0 && (m.streaming || atBottom) {
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

	// Run errors surface here, pinned above the composer, instead of being
	// appended to the scrollback where they would scroll out of view.
	if m.errMsg != "" {
		sections = append(sections, styleError().Render("  ✗ "+m.errMsg))
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
	if m.state == stateProcessing {
		left := m.spinner.view()
		if m.compacting {
			left += styleDim().Render(" 压缩中…")
		}
		// Spinner frames tick ~100ms, so the frame delta is deciseconds.
		elapsed := (m.spinner.frame - m.turnStartFrame) / 10
		right := styleDim().Render(fmt.Sprintf("esc to interrupt · %s · %ds", m.model, elapsed))
		return statusLine(m.mainWidth, left, right)
	}
	left := styleDim().Render(m.modeLabel())
	return statusLine(m.mainWidth, left, m.contextRight())
}

// modeLabel returns the current agent mode for the status bar.
func (m *Model) modeLabel() string {
	if mode := m.agent.Mode(); mode != "" {
		return mode
	}
	return "ready"
}

// contextRight builds the right-aligned status segment (ctx% + model + cost).
func (m *Model) contextRight() string {
	seg := m.model
	if m.contextPct >= 0 {
		seg = fmt.Sprintf("ctx %d%% · %s", m.contextPct, seg)
	}
	if m.totalCost > 0 {
		seg += fmtCost(m.totalCost)
	}
	return styleDim().Render(seg)
}

// updateContextStats refreshes the cached context budget usage from the agent.
// Stats() walks every block and estimates tokens, so it is too expensive for
// the per-frame View path; it runs only at turn/compaction boundaries. When the
// agent reports no drop limit (tests, unconfigured budget) the cache is reset
// to -1 so the status line hides the segment.
func (m *Model) updateContextStats() {
	if m.agent == nil {
		m.contextPct = -1
		return
	}
	stats := m.agent.Stats()
	drop := stats["drop_limit"]
	if drop <= 0 {
		m.contextPct = -1
		return
	}
	m.contextPct = stats["total"] * 100 / drop
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

// startupView renders the animated brand banner over an empty body.
func (m Model) startupView() string {
	hdr := renderHeader(m.header, m.headerFrame, m.width)
	hint := styleDim().Render("  Press any key to begin")
	body := lipgloss.JoinVertical(lipgloss.Center, hdr, "", hint)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

// fmtCost formats a dollar cost segment (" · $0.01"). Costs at or above a cent
// use two decimals; smaller costs keep four so a cheap-but-nonzero turn doesn't
// read as "$0.00".
func fmtCost(c float64) string {
	if c >= 0.01 {
		return fmt.Sprintf(" · $%.2f", c)
	}
	return fmt.Sprintf(" · $%.4f", c)
}
