package agentsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/memory"
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

func TestContextManagerBlocksSetCacheHint(t *testing.T) {
	cfg := config.Config{Context: config.ContextConfig{MinRecent: 1}}
	mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, "", nil, "project context here", "skill block here", nil, nil)

	pd, ok := mgr.GetBlock(contextmgr.BlockProjectDocs, "project_docs")
	if !ok {
		t.Fatal("missing project_docs block")
	}
	if pd.CacheHint != contextmgr.CacheHintBreakpoint {
		t.Fatalf("project_docs block should have CacheHintBreakpoint, got %q", pd.CacheHint)
	}

	sk, ok := mgr.GetBlock(contextmgr.BlockSkills, "skills")
	if !ok {
		t.Fatal("missing skills block")
	}
	if sk.CacheHint != contextmgr.CacheHintBreakpoint {
		t.Fatalf("skills block should have CacheHintBreakpoint, got %q", sk.CacheHint)
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

func TestContextManagerMemoryBlocks(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := os.WriteFile(filepath.Join(home, ".lcoder", "memory", "USER.md"), []byte("User prefers Chinese."), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".lcoder", "memory", "MEMORY.md"), []byte("Project uses Go modules."), 0640); err != nil {
		t.Fatal(err)
	}

	store, err := memory.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Context: config.ContextConfig{MinRecent: 1}, Memory: config.MemoryConfig{Enabled: true}}
	mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, "", nil, "", "", nil, store)

	if _, ok := mgr.GetBlock(contextmgr.BlockUserProfile, "user_profile"); !ok {
		t.Fatal("missing user_profile block")
	}
	if _, ok := mgr.GetBlock(contextmgr.BlockMemory, "memory"); !ok {
		t.Fatal("missing memory block")
	}

	merged := mgr.SystemPrompt()
	if !strings.Contains(merged, "User prefers Chinese") || !strings.Contains(merged, "Go modules") {
		t.Fatalf("system prompt should include memory text, got:\n%s", merged)
	}
}

func TestContextManagerDynamicRecallSkipsStaticMemory(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".lcoder", "memory"), 0750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := os.WriteFile(filepath.Join(home, ".lcoder", "memory", "USER.md"), []byte("User prefers Chinese."), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".lcoder", "memory", "MEMORY.md"), []byte("Global memory entry."), 0640); err != nil {
		t.Fatal(err)
	}

	store, err := memory.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Context: config.ContextConfig{MinRecent: 1},
		Memory:  config.MemoryConfig{Enabled: true, DynamicRecall: true},
	}
	mgr := NewContextManager(cfg, config.TokenBudget{MaxTotal: 100000, TargetTotal: 90000, ReserveOutput: 8192}, "", nil, "", "", nil, store)

	if _, ok := mgr.GetBlock(contextmgr.BlockUserProfile, "user_profile"); !ok {
		t.Fatal("missing user_profile block")
	}
	if _, ok := mgr.GetBlock(contextmgr.BlockMemory, "memory"); ok {
		t.Fatal("static memory block should be omitted when dynamic recall is enabled")
	}
}
