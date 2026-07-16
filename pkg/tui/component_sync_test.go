package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestComponentMsgRoutesToComponent(t *testing.T) {
	m := &Model{}
	m.components = []components.BlockComponent{
		components.NewToolResultComponent("t1", "bash", `{"cmd":"ls"}`, "line1\nline2\nline3", false, false, time.Time{}, 0),
	}
	before := m.components[0].Render(40, false)
	m.Update(components.ComponentMsg{ID: "t1", Msg: components.ToggleExpandedMsg{}})
	after := m.components[0].Render(40, false)
	if before == after {
		t.Fatalf("component did not update after ComponentMsg:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestPatchAssistantRebuildsComponent(t *testing.T) {
	m := &Model{streamMsgID: "a1"}
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "old"}}
	m.components = componentsFromBlocks(m.blocks)
	m.patchAssistant("new")
	if m.blocks[0].raw != "new" {
		t.Fatalf("block raw = %q, want new", m.blocks[0].raw)
	}
	ac, ok := m.components[0].(*components.AssistantComponent)
	if !ok {
		t.Fatalf("component type = %T, want *components.AssistantComponent", m.components[0])
	}
	if !strings.Contains(ac.Render(40, false), "new") {
		t.Fatalf("component did not update content: %q", ac.Render(40, false))
	}
}

func TestCommitAssistantRebuildsComponent(t *testing.T) {
	m := &Model{}
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "draft"}}
	m.components = componentsFromBlocks(m.blocks)
	m.commitAssistant("a1", "final", "think", nil)
	if m.blocks[0].raw != "final" {
		t.Fatalf("block raw = %q, want final", m.blocks[0].raw)
	}
	ac, ok := m.components[0].(*components.AssistantComponent)
	if !ok {
		t.Fatalf("component type = %T, want *components.AssistantComponent", m.components[0])
	}
	rendered := ac.Render(40, true)
	if !strings.Contains(rendered, "final") {
		t.Fatalf("component missing content: %q", rendered)
	}
	if !strings.Contains(rendered, "think") {
		t.Fatalf("component missing thinking: %q", rendered)
	}
}

func TestFinishToolRebuildsComponent(t *testing.T) {
	m := &Model{}
	m.blocks = []block{{kind: components.BlockTool, id: "t1", toolName: "bash"}}
	m.components = componentsFromBlocks(m.blocks)
	m.finishTool("t1", "bash", models.NewToolExecutionResultText("done"), false)
	if m.blocks[0].toolResult != "done" {
		t.Fatalf("block toolResult = %q, want done", m.blocks[0].toolResult)
	}
	tr, ok := m.components[0].(*components.ToolResultComponent)
	if !ok {
		t.Fatalf("component type = %T, want *components.ToolResultComponent", m.components[0])
	}
	if !strings.Contains(tr.Render(40, false), "done") {
		t.Fatalf("component did not update content: %q", tr.Render(40, false))
	}
}

func TestComponentMsgSyncsExpandedToBlock(t *testing.T) {
	m := &Model{}
	m.blocks = []block{{kind: components.BlockTool, id: "t1", toolName: "bash", toolResult: "line1\nline2\nline3"}}
	m.components = componentsFromBlocks(m.blocks)

	m.Update(components.ComponentMsg{ID: "t1", Msg: components.ToggleExpandedMsg{}})
	if !m.blocks[0].expanded {
		t.Fatal("expected block.expanded to be synced to true")
	}
	tr := m.components[0].(*components.ToolResultComponent)
	if !tr.Expanded() {
		t.Fatal("expected component Expanded() to be true")
	}
}

func TestCommitAssistantPreservesExpanded(t *testing.T) {
	m := &Model{}
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "draft"}}
	m.components = componentsFromBlocks(m.blocks)
	m.blocks[0].expanded = true
	m.components[0] = toComponent(m.blocks[0])

	m.commitAssistant("a1", "final", "think", nil)
	if !m.blocks[0].expanded {
		t.Fatal("expected block.expanded to remain true")
	}
	ac := m.components[0].(*components.AssistantComponent)
	if !ac.Expanded() {
		t.Fatal("expected rebuilt assistant component Expanded() to be true")
	}
}

func TestFinishToolPreservesExpanded(t *testing.T) {
	m := &Model{}
	m.blocks = []block{{kind: components.BlockTool, id: "t1", toolName: "bash"}}
	m.components = componentsFromBlocks(m.blocks)
	m.blocks[0].expanded = true
	m.components[0] = toComponent(m.blocks[0])

	m.finishTool("t1", "bash", models.NewToolExecutionResultText("done"), false)
	if !m.blocks[0].expanded {
		t.Fatal("expected block.expanded to remain true")
	}
	tr := m.components[0].(*components.ToolResultComponent)
	if !tr.Expanded() {
		t.Fatal("expected rebuilt tool component Expanded() to be true")
	}
}
