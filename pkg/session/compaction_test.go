package session

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func newTestSession(t *testing.T, msgs ...models.AgentMessage) *Session {
	t.Helper()
	store := NewStore(t.TempDir())
	sess, err := store.Create("/tmp/proj")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, m := range msgs {
		if err := sess.Append(m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return sess
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

// E1: 压缩条目追加后原始消息全部保留,文件行数 = 原消息数 + 1。
func TestAppendCompactionEntryKeepsRawMessages(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("one"), models.AssistantMessage("two"),
		models.UserMessage("three"), models.AssistantMessage("four"),
	}
	sess := newTestSession(t, msgs...)
	before := countLines(t, sess.Path)

	if err := sess.AppendCompactionEntry("SUMMARY TEXT", msgs[2].ID, 1234); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	if got := countLines(t, sess.Path); got != before+1 {
		t.Fatalf("expected %d lines, got %d", before+1, got)
	}
	// 重新加载:四条原始消息 + 条目都在。
	store := NewStore("")
	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != len(msgs)+1 {
		t.Fatalf("expected %d messages on disk, got %d", len(msgs)+1, len(loaded.Messages))
	}
	var entry models.AgentMessage
	for _, m := range loaded.Messages {
		if IsCompactionEntry(m) {
			entry = m
		}
	}
	if entry.ID == "" {
		t.Fatal("compaction entry not found after reload")
	}
	if got, _ := entry.Metadata["first_kept_entry_id"].(string); got != msgs[2].ID {
		t.Fatalf("first_kept_entry_id mismatch: %q", got)
	}
	if tb, _ := entry.Metadata["tokens_before"].(float64); int(tb) != 1234 {
		t.Fatalf("tokens_before mismatch: %v", entry.Metadata["tokens_before"])
	}
}

// E2: 视图 = 摘要 + firstKeptEntryId 起的消息;原始旧消息不在视图中。
func TestEffectiveMessagesWithEntry(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("one"), models.AssistantMessage("two"),
		models.UserMessage("three"), models.AssistantMessage("four"),
	}
	sess := newTestSession(t, msgs...)
	if err := sess.AppendCompactionEntry("SUM", msgs[2].ID, 100); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	view := sess.EffectiveMessages()
	if len(view) != 3 { // 摘要 + three + four
		t.Fatalf("expected 3 messages in view, got %d: %v", len(view), view)
	}
	if v, ok := view[0].Metadata["compacted"].(bool); !ok || !v {
		t.Fatal("view head must be a compacted summary")
	}
	if !strings.Contains(view[0].Text(), "SUM") {
		t.Fatalf("summary text missing: %q", view[0].Text())
	}
	if view[1].Text() != "three" || view[2].Text() != "four" {
		t.Fatalf("kept tail wrong: %q %q", view[1].Text(), view[2].Text())
	}
}

// E2b: 多次压缩——只用最新条目;第二次 firstKept 指向 entry 之后的消息。
func TestEffectiveMessagesMultipleEntries(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("one"), models.AssistantMessage("two"),
	}
	sess := newTestSession(t, msgs...)
	_ = sess.AppendCompactionEntry("SUM1", msgs[0].ID, 100)
	m3, m4 := models.UserMessage("three"), models.AssistantMessage("four")
	_ = sess.Append(m3)
	_ = sess.Append(m4)
	_ = sess.AppendCompactionEntry("SUM2", m3.ID, 200)

	view := sess.EffectiveMessages()
	if len(view) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(view))
	}
	if !strings.Contains(view[0].Text(), "SUM2") {
		t.Fatalf("must use newest entry, got %q", view[0].Text())
	}
	if view[1].Text() != "three" {
		t.Fatalf("kept must start at SUM2's firstKept, got %q", view[1].Text())
	}
}

// E3: 悬挂 firstKeptEntryId → 回退到条目之后的所有消息。
func TestEffectiveMessagesDanglingFirstKept(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	_ = sess.AppendCompactionEntry("SUM", "nonexistent-id", 100)
	m3 := models.UserMessage("three")
	_ = sess.Append(m3)
	view := sess.EffectiveMessages()
	if len(view) != 2 || view[1].Text() != "three" {
		t.Fatalf("dangling firstKept must fall back to post-entry messages: %v", view)
	}
}

// E4: 分支场景——在 main 压缩后,fork 出的分支消息不受影响。
func TestEffectiveMessagesBranchCoexistence(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	branchID, err := sess.Fork(msgs[0].ID)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	bm := models.UserMessage("branch msg")
	_ = sess.Append(bm)
	if err := sess.SwitchBranch(mainBranch); err != nil {
		t.Fatalf("switch: %v", err)
	}
	_ = sess.AppendCompactionEntry("SUM", msgs[1].ID, 100)

	// main 视图正常。
	if view := sess.EffectiveMessages(); len(view) != 2 {
		t.Fatalf("main view: expected 2, got %d", len(view))
	}
	// 切回分支:分支消息仍在,且无 compaction entry 在分支链上。
	if err := sess.SwitchBranch(branchID); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	view := sess.EffectiveMessages()
	var texts []string
	for _, m := range view {
		texts = append(texts, m.Text())
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "branch msg") {
		t.Fatalf("branch message lost: %v", texts)
	}
}

// E5: 无条目的旧 session 与旧 Replace 格式(含 compacted 摘要消息)行为不变。
func TestEffectiveMessagesLegacySessions(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	if view := sess.EffectiveMessages(); len(view) != 2 {
		t.Fatalf("legacy linear session must pass through, got %d", len(view))
	}

	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: "[Summary of earlier conversation]\n\nold"}).
		WithMetadata("compacted", true)
	sess2 := newTestSession(t, summary, models.UserMessage("after"))
	if view := sess2.EffectiveMessages(); len(view) != 2 {
		t.Fatalf("legacy replaced session must pass through, got %d", len(view))
	}
}

// E6: 条目在后续 Append 触发的整文件 Save 重写后仍然保留,不重复不丢失。
func TestAppendCompactionEntrySurvivesSubsequentSave(t *testing.T) {
	msgs := []models.AgentMessage{models.UserMessage("one"), models.AssistantMessage("two")}
	sess := newTestSession(t, msgs...)
	if err := sess.AppendCompactionEntry("SUM", msgs[1].ID, 100); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	// 再 Append 一条消息,触发整文件 Save 重写。
	if err := sess.Append(models.UserMessage("three")); err != nil {
		t.Fatalf("append three: %v", err)
	}

	if got := countLines(t, sess.Path); got != 4 {
		t.Fatalf("expected 4 lines on disk, got %d", got)
	}
	store := NewStore("")
	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 4 {
		t.Fatalf("expected 4 messages on disk, got %d", len(loaded.Messages))
	}
	entries := 0
	for _, m := range loaded.Messages {
		if IsCompactionEntry(m) {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("expected exactly one compaction entry, got %d", entries)
	}
}

// E7: 在条目之后 fork 出的分支,其 EffectiveMessages 视图一致地应用压缩。
func TestEffectiveMessagesForkAfterEntry(t *testing.T) {
	one, two := models.UserMessage("one"), models.AssistantMessage("two")
	sess := newTestSession(t, one, two)
	if err := sess.AppendCompactionEntry("SUM", two.ID, 100); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	three := models.UserMessage("three")
	if err := sess.Append(three); err != nil {
		t.Fatalf("append three: %v", err)
	}
	// 从 three 处 fork,并在分支上追加消息。
	if _, err := sess.Fork(three.ID); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := sess.Append(models.UserMessage("branch msg")); err != nil {
		t.Fatalf("append branch msg: %v", err)
	}

	view := sess.EffectiveMessages()
	var texts []string
	for _, m := range view {
		texts = append(texts, m.Text())
	}
	joined := strings.Join(texts, "|")
	// 视图必须包含压缩摘要(作为头部)、three 以及 branch msg。
	if v, ok := view[0].Metadata["compacted"].(bool); !ok || !v {
		t.Fatalf("forked view head must be a compacted summary: %v", texts)
	}
	if !strings.Contains(joined, "three") {
		t.Fatalf("forked view must include three: %v", texts)
	}
	if !strings.Contains(joined, "branch msg") {
		t.Fatalf("forked view must include branch msg: %v", texts)
	}
}
