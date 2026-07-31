package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/mcp"
)

// ExtensionsPanelModel renders MCP server status.
type ExtensionsPanelModel struct {
	MCPServers []mcp.ServerStatus
	Visible    bool
}

// Toggle visibility.
func (m *ExtensionsPanelModel) Toggle() {
	m.Visible = !m.Visible
}

// SetMCPServers updates MCP server status.
func (m *ExtensionsPanelModel) SetMCPServers(statuses []mcp.ServerStatus) {
	m.MCPServers = statuses
}

// View renders the extensions panel.
func (m ExtensionsPanelModel) View(width, maxHeight int) string {
	if !m.Visible {
		return ""
	}

	accent := styleAccent().Bold(true)
	var lines []string
	lines = append(lines, accent.Render("Extension Tools"))

	if len(m.MCPServers) == 0 {
		lines = append(lines, styleDim().Render("No extensions configured."))
	}

	if len(m.MCPServers) > 0 {
		lines = append(lines, accent.Render("MCP Servers"))
		for _, s := range m.MCPServers {
			status := "✓"
			statusStyle := styleSuccess()
			if !s.Connected {
				status = "✗"
				statusStyle = styleError()
			}
			name := accent.Render(s.Name)
			info := fmt.Sprintf("%s %s · %d tools", statusStyle.Render(status), s.Info.Name, s.ToolCount)
			if s.Error != "" {
				info = fmt.Sprintf("%s %s", statusStyle.Render(status), styleError().Render(s.Error))
			}
			lines = append(lines, fmt.Sprintf("%s\n  %s", name, info))
		}
	}

	if len(lines) > maxHeight && maxHeight > 0 {
		lines = lines[:maxHeight-1]
		lines = append(lines, styleDim().Render("  ..."))
	}

	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint).
		Padding(0, 1).
		Width(width)

	return panelStyle.Render(strings.Join(lines, "\n"))
}
