package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/testutil"
)

// 选中条目按 r 进入内联重命名,Enter 写回标题,Esc 取消不写盘。
func TestPickerInlineRename(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.NewStore(dir).Create(".")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(models.UserMessage("old derived title"))
	p := NewSessionPicker(&testutil.FakeSessionStore{Sessions: []*session.Session{sess}, Session: sess}, ".", "select", nil)

	// 按 r 进入重命名,预填当前 DisplayTitle。
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
	reloaded, err := session.NewStore(dir).LoadByID(".", sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Title(); got != "old derived titlerenamed" {
		t.Fatalf("persisted title = %q", got)
	}

	// 再次进入并 Esc:不写盘。
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.Renaming() {
		t.Fatal("esc must exit renaming mode")
	}
	if got := reloaded.Title(); got != "old derived titlerenamed" {
		t.Fatalf("esc must not persist, title = %q", got)
	}
}
