package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
)

// /thinking 无参数 → 弹出横向分段选择器（effortSel 非空）。
func TestThinkingCommandOpensPicker(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.cfg.Provider = "openai"
	m.cfg.Model = "gpt-4o-mini"
	m.model = "openai/gpt-4o-mini"
	m.completedTurns = 1 // 有对话历史 → 应显示缓存警告

	m.dispatchSlash("/thinking")

	if m.effortSel == nil {
		t.Fatal("expected effort picker after bare /thinking")
	}
	out := m.effortSel.render(60)
	if !strings.Contains(out, "[") || !strings.Contains(out, "]") {
		t.Fatalf("picker should render segments, got %q", out)
	}
	// 有历史 → 警告行存在。
	if !strings.Contains(m.effortWarning(), "cache") {
		t.Fatalf("expected cache warning with history, got %q", m.effortWarning())
	}
}

// /thinking <effort> 会话级生效，不落盘。
func TestThinkingCommandSessionOnly(t *testing.T) {
	m, ag, _ := newTestModel()
	m.state = stateInput
	m.cfg.Provider = "openai"
	m.cfg.Model = "gpt-4o-mini"
	m.model = "openai/gpt-4o-mini"

	m.dispatchSlash("/thinking high")

	if ag.SwitchedThinking != "high" {
		t.Fatalf("agent.SwitchedThinking = %q, want high", ag.SwitchedThinking)
	}
	if m.effortSel != nil {
		t.Fatal("effort picker should close after explicit effort")
	}
}

// /thinking <effort> --persist 会话级 + 写 config.yaml。
func TestThinkingCommandPersist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	m, ag, _ := newTestModel()
	m.state = stateInput
	m.cfg.Provider = "openai"
	m.cfg.Model = "gpt-4o-mini"
	m.model = "openai/gpt-4o-mini"

	m.dispatchSlash("/thinking low --persist")

	if ag.SwitchedThinking != "low" {
		t.Fatalf("agent.SwitchedThinking = %q, want low", ag.SwitchedThinking)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thinking != "low" {
		t.Fatalf("persisted thinking = %q, want low", cfg.Thinking)
	}
}

// Alt+S 提交 → 仅会话；Enter 提交 → 持久化。
func TestEffortPickerCommitPaths(t *testing.T) {
	m, ag, _ := newTestModel()
	m.state = stateInput
	m.cfg.Provider = "openai"
	m.cfg.Model = "gpt-4o-mini"
	m.model = "openai/gpt-4o-mini"

	m.effortSel = newEffortSelector([]string{"off", "low", "medium", "high"}, "low", "")
	m.effortSel.activeIndex = 2 // medium
	m.applyEffortSelection(false)
	if ag.SwitchedThinking != "medium" {
		t.Fatalf("Alt+S should apply session-only, got %q", ag.SwitchedThinking)
	}
	if m.effortSel != nil {
		t.Fatal("picker should close after Alt+S")
	}
}

// 状态栏显示 think:<effort>。
func TestStatusLineShowsThinkingEffort(t *testing.T) {
	m, ag, _ := newTestModel()
	ag.ThinkingVal = "high"
	m.state = stateInput
	m.width = 80
	m.height = 24

	label := m.modeLabel()
	if !strings.Contains(label, "think:high") {
		t.Fatalf("modeLabel should show think:high, got %q", label)
	}
}

// 无 llmClient（测试环境）时 /thinking 回退：裸命令应给出提示而非崩溃。
func TestThinkingWithoutLLMClientFallsBack(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.cfg.Provider = ""
	m.cfg.Model = ""
	m.model = ""

	m.dispatchSlash("/thinking")
	// 无 active model → 不弹选择器，显示文本提示。
	if m.effortSel != nil {
		t.Fatalf("no active model should not open picker, got %q", m.effortSel.render(40))
	}
	if !m.cmdPanel.visible {
		t.Fatal("expected text panel fallback")
	}
}
