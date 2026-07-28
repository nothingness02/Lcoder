package agentsetup

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
)

func TestBuildSystemPrompt(t *testing.T) {
	p := BuildSystemPrompt()
	if p == "" {
		t.Fatal("expected non-empty prompt")
	}
	if strings.Contains(p, "ctx") || strings.Contains(p, "skills") {
		t.Fatal("context and skills must not be embedded in the base system prompt; they live in their own blocks")
	}
	if !strings.Contains(p, "Lcoder") {
		t.Fatal("expected persona line in prompt")
	}
	// The operating contract must state the tool-grounding discipline and the
	// natural-completion convention the loop relies on.
	if !strings.Contains(p, "tool") {
		t.Fatal("expected tool-usage guidance in prompt")
	}
	if !strings.Contains(p, "NO tool calls") {
		t.Fatal("expected completion convention (final message with no tool calls)")
	}
	if strings.Contains(p, "\n\n\n") {
		t.Fatal("unexpected empty-block spacing in prompt")
	}
}

// TestBuildSystemPromptBlockIndependence ensures context/skills blocks are
// provided separately and assembled by the context manager without duplicating
// them inside the system block.
func TestContextManagerBlocks(t *testing.T) {
	cfg := config.Config{Context: config.ContextConfig{MinRecent: 1}}
	mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, "", nil, "project context here", "skill block here", nil, nil)

	sys, ok := mgr.GetBlock(contextmgr.BlockSystem, "system")
	if !ok {
		t.Fatal("missing system block")
	}
	sysText := sys.Text()
	if strings.Contains(sysText, "project context here") || strings.Contains(sysText, "skill block here") {
		t.Fatal("system block should not duplicate context/skills")
	}

	if _, ok := mgr.GetBlock(contextmgr.BlockProjectDocs, "project_docs"); !ok {
		t.Fatal("missing project_docs block")
	}
	if _, ok := mgr.GetBlock(contextmgr.BlockSkills, "skills"); !ok {
		t.Fatal("missing skills block")
	}

	merged := mgr.SystemPrompt()
	if !strings.Contains(merged, "project context here") || !strings.Contains(merged, "skill block here") {
		t.Fatal("merged system prompt should still include context and skills from their own blocks")
	}
}


