package tui

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agentapi"
)

// /rename 设置显式标题;无参时展示当前标题。
func TestRenameCommandSetsAndShowsTitle(t *testing.T) {
	ag := &fakeAgent{
		SessionIDVal: "abc123",
		SessionsList: []agentapi.SessionInfo{{ID: "abc123", Title: "旧标题"}},
	}
	m := newTestCoreModel(ag)

	m.dispatchSlash("/rename 修复登录页样式")
	if got := ag.RenamedSessions["abc123"]; got != "修复登录页样式" {
		t.Fatalf("RenameSession title = %q", got)
	}

	// 无参:展示当前标题(来自 ListSessions)。
	m.dispatchSlash("/rename")
	if !m.cmdPanel.visible || !strings.Contains(m.cmdPanel.text, "current title: 修复登录页样式") {
		t.Fatalf("expected current title panel, got %+v", m.cmdPanel)
	}
}
