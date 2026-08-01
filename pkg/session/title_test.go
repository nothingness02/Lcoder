package session

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// 无显式标题时,标题取最后一条用户消息(空白折叠、截断)。
func TestDisplayTitleFromLatestUserMessage(t *testing.T) {
	s := &Session{ID: "abc12345"}
	s.Messages = []models.AgentMessage{
		models.UserMessage("first question"),
		models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "answer"}),
		models.UserMessage("second   question\nwith newline"),
	}
	if got := s.DisplayTitle(); got != "second question with newline" {
		t.Fatalf("DisplayTitle = %q", got)
	}
}

// 显式标题优先;持久化后重载仍可读回。
func TestSetTitlePersists(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s, err := store.Create(".")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Append(models.UserMessage("auto title candidate"))
	if err := s.SetTitle("修复登录页样式"); err != nil {
		t.Fatal(err)
	}
	if got := s.DisplayTitle(); got != "修复登录页样式" {
		t.Fatalf("DisplayTitle = %q", got)
	}

	reloaded, err := store.LoadByID(".", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.DisplayTitle(); got != "修复登录页样式" {
		t.Fatalf("reloaded DisplayTitle = %q", got)
	}
}

// 没有标题也没有消息时退回 session ID;长输入按 rune 截断。
func TestDisplayTitleFallbacks(t *testing.T) {
	s := &Session{ID: "abc12345"}
	if got := s.DisplayTitle(); got != "abc12345" {
		t.Fatalf("empty session must fall back to ID, got %q", got)
	}

	long := strings.Repeat("长", 60)
	s.Messages = []models.AgentMessage{models.UserMessage(long)}
	got := s.DisplayTitle()
	if r := []rune(got); len(r) > 41 {
		t.Fatalf("title must be truncated, got %d runes", len(r))
	}
}
