package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/session"
)

// /rename 设置显式标题;无参时展示当前标题(默认取最后用户消息)。
func TestRenameCommandSetsAndShowsTitle(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.NewStore(dir).Create(".")
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(events.New(), &fakeAgent{}, sess, &fakeSessionStore{}, ".", sess.ID,
		"openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)

	m.dispatchSlash("/rename 修复登录页样式")
	if got := sess.DisplayTitle(); got != "修复登录页样式" {
		t.Fatalf("DisplayTitle = %q", got)
	}

	// 新会话无显式标题:退回最后一条用户消息。
	sess2, _ := session.NewStore(t.TempDir()).Create(".")
	if got := sess2.DisplayTitle(); got != sess2.ID {
		t.Fatalf("empty session must fall back to ID, got %q", got)
	}
}
