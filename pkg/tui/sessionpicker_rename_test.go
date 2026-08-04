package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/testutil"
)

// 选中条目按 r 进入内联重命名,Enter 经 CoreAPI.RenameSession 写回标题,Esc 取消不写入。
func TestPickerInlineRename(t *testing.T) {
	ag := &testutil.FakeAgent{
		SessionsList: []agentapi.SessionInfo{{ID: "s1", Title: "old derived title", MessageCount: 1, CWD: "."}},
	}
	p := NewSessionPicker(ag)

	// 按 r 进入重命名,预填当前标题。
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !p.Renaming() {
		t.Fatal("r must enter renaming mode")
	}
	if got := p.RenameValue(); got != "old derived title" {
		t.Fatalf("prefill = %q", got)
	}

	// 输入新标题并 Enter。
	for _, r := range "renamed" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.Renaming() {
		t.Fatal("enter must exit renaming mode")
	}
	if got := ag.RenamedSessions["s1"]; got != "old derived titlerenamed" {
		t.Fatalf("renamed title = %q", got)
	}

	// 再次进入并 Esc:不再调用 RenameSession。
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.Renaming() {
		t.Fatal("esc must exit renaming mode")
	}
	if len(ag.RenamedSessions) != 1 {
		t.Fatalf("esc must not rename, calls = %v", ag.RenamedSessions)
	}
}
