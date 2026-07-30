package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/agent"
)

// rebuildViewport re-renders all blocks into the viewport and pins to bottom
// when the user is already at the bottom (sticky bottom — a user who scrolled
// up is never yanked back, even mid-stream). When a block is focused, the
// viewport is scrolled to keep that block visible. The virtual window is
// materialized for the TARGET offset (computed before SetContent), so the
// final view never lands in a region of blank placeholders. Every rebuild
// counts as a flush: it clears the scheduler's dirty flag and stamps the
// flush time, so the frame scheduler never re-renders right behind it.
func (m *Model) rebuildViewport() {
	layouts := layoutComponents(m.components, m.viewport.Width, m.toolsExpanded, m.focusedBlockIndex)
	total := maxTotalHeight(layouts)

	atBottom := m.viewport.AtBottom()
	scrollY := m.viewport.YOffset
	switch {
	case m.focusedBlockIndex >= 0 && m.focusedBlockIndex < len(layouts):
		scrollY = clamp(layouts[m.focusedBlockIndex].offset, 0, total-m.viewport.Height)
	case atBottom:
		scrollY = max(0, total-m.viewport.Height)
	}

	m.viewport.SetContent(buildVirtualContent(layouts, m.viewport.Height, scrollY, m.toolsExpanded, m.focusedBlockIndex))
	switch {
	case m.focusedBlockIndex >= 0 && m.focusedBlockIndex < len(layouts):
		m.viewport.SetYOffset(scrollY)
	case atBottom:
		m.viewport.GotoBottom()
	}
	m.rebuilds++
	m.sched.dirty = false
	m.sched.lastFlush = m.now()
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
// Interactive panels (permission confirm, session picker, extensions, provider)
// occupy the region instead of the composer — kimi-code's editor-replacement
// pattern: the transcript stays visible above, every panel lives in the same
// place, framed by the same top border.
func (m *Model) bottomRegion() string {
	var sections []string

	switch m.state {
	case stateConfirm:
		return m.confirm.View(m.mainWidth)
	case stateSessionPicker:
		return panelFrame(m.mainWidth, m.picker.View())
	case stateExtensions:
		return panelFrame(m.mainWidth, m.extPanel.View(m.mainWidth-4, m.height/3))
	case stateProvider:
		return panelFrame(m.mainWidth, m.renderProviderPanel())
	}

	if m.taskSidebarVisible() {
		sections = append(sections, panelFrame(m.mainWidth, renderTaskStrip(m.tasks, m.mainWidth)))
	}

	if m.menuVisible {
		matches := menuMatches(m.input.Value())
		sections = append(sections, renderMenu(matches, m.menuSelected, m.mainWidth))
	} else if m.fileMenuVisible {
		if m.fileMenuIndexing {
			sections = append(sections, renderIndexingHint())
		} else {
			sections = append(sections, renderFileMenu(m.fileMenuItems, m.fileMenuSelected, m.mainWidth))
		}
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

// modeLabel returns the current agent mode for the status bar, with a goal
// marker while a goal is being pursued.
func (m *Model) modeLabel() string {
	label := "ready"
	if mode := m.agent.Mode(); mode != "" {
		label = mode
	}
	if g := m.agent.Goal(); g != nil && g.Status == agent.GoalActive {
		label += " · goal"
	}
	return label
}

// contextRight builds the right-aligned status segment. Context usage leads
// in kimi-code's shape: `context: 42% (86.5k/200k)`, then model and cost.
func (m *Model) contextRight() string {
	seg := m.model
	if m.contextPct >= 0 {
		seg = fmt.Sprintf("context: %d%% (%s/%s) · %s",
			m.contextPct, abbrevTokens(m.contextUsedTok), abbrevTokens(m.contextLimitTok), seg)
	}
	if m.totalCost > 0 {
		seg += fmtCost(m.totalCost)
	}
	return styleDim().Render(seg)
}

// abbrevTokens renders a token count with 1024-based k/M suffixes
// (kimi-code's formatTokenCount: 86.5k / 977k / 1.5M).
func abbrevTokens(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fk", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d", n)
	}
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
	m.contextUsedTok = stats["total"]
	m.contextLimitTok = drop
}

// panelFrame wraps a bottom-strip panel in the shared top-border frame.
func panelFrame(width int, content string) string {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(width).
		Render(content)
}

// View implements tea.Model.
func (m Model) View() string {
	switch m.state {
	case stateStartup:
		return m.startupView()
	}

	top := m.viewport.View()
	bottom := m.bottomRegion()
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
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
