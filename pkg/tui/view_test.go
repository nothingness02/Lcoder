package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// Panels (extensions / provider) render as a framed bottom strip with the
// transcript visible above, not as a full-screen replacement — the unified
// editor-replacement layout.
func TestPanelsRenderAsBottomStrip(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
	m.width = 80
	m.height = 24
	m.mainWidth = 80
	m.blocks = append(m.blocks, block{kind: components.BlockUser, raw: "hello transcript"})
	m.components = componentsFromBlocks(m.blocks)
	m.updateSizes()

	m.extPanel.Visible = true
	m.extPanel.MCPServers = []mcp.ServerStatus{{Name: "deploy", Connected: true}}
	m.state = stateExtensions
	out := m.View()
	if !strings.Contains(out, "hello transcript") {
		t.Fatalf("transcript should stay visible above the panel:\n%s", out)
	}
	if !strings.Contains(out, "deploy") {
		t.Fatalf("extensions panel should render in the bottom strip:\n%s", out)
	}

	m.extPanel.Visible = false
	m.openProviderPanel()
	m.mainWidth = 80
	out = m.View()
	if !strings.Contains(out, "hello transcript") {
		t.Fatalf("transcript should stay visible above the provider panel:\n%s", out)
	}
	if !strings.Contains(out, "Select a provider") {
		t.Fatalf("provider panel should render in the bottom strip:\n%s", out)
	}
}
