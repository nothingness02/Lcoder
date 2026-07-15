package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestPatchAssistantRebuildsComponent(t *testing.T) {
	m := &Model{streamMsgID: "a1"}
	m.blocks = []block{{kind: components.BlockAssistant, id: "a1", raw: "old"}}
	m.components = componentsFromBlocks(m.blocks)
	m.patchAssistant("new")
	if m.blocks[0].raw != "new" {
		t.Fatalf("block raw = %q, want new", m.blocks[0].raw)
	}
	fb, ok := m.components[0].(fallbackComponent)
	if !ok {
		t.Fatalf("component type = %T, want fallbackComponent", m.components[0])
	}
	if fb.b.raw != "new" {
		t.Fatalf("component raw = %q, want new", fb.b.raw)
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
	fb := m.components[0].(fallbackComponent)
	if fb.b.raw != "final" || fb.b.thinking != "think" {
		t.Fatalf("component not rebuilt: raw=%q thinking=%q", fb.b.raw, fb.b.thinking)
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
	fb := m.components[0].(fallbackComponent)
	if fb.b.toolResult != "done" {
		t.Fatalf("component toolResult = %q, want done", fb.b.toolResult)
	}
}
