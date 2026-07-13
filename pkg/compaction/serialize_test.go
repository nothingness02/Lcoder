package compaction

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestSerializeConversationRoles(t *testing.T) {
	msgs := []models.AgentMessage{
		models.UserMessage("fix the bug"),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "let me look"},
			models.ToolCallContent{ID: "c1", Name: "read", Arguments: map[string]any{"path": "foo.go"}}),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1", Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: "package main"}},
		}),
	}
	out := SerializeConversation(msgs, 2000)
	for _, want := range []string{"[User]: fix the bug", "[Assistant]: let me look", `[Assistant tool calls]: read(path="foo.go")`, "[Tool result]: package main"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSerializeConversationTruncatesToolResults(t *testing.T) {
	big := strings.Repeat("z", 10000)
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1", Name: "read",
			Content: []models.ContentPart{models.TextContent{Text: big}},
		}),
	}
	out := SerializeConversation(msgs, 2000)
	if len(out) > 2200 {
		t.Fatalf("expected truncated output, got %d chars", len(out))
	}
	if !strings.Contains(out, "truncated 8000 chars") {
		t.Fatalf("missing truncation marker:\n%s", out[:200])
	}
}

func TestSerializeConversationMultiPartToolResult(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleToolResult,
			models.ToolResultContent{
				ToolCallID: "c1", Name: "read",
				Content: []models.ContentPart{models.TextContent{Text: "AAA"}},
			},
			models.ToolResultContent{
				ToolCallID: "c2", Name: "read",
				Content: []models.ContentPart{models.TextContent{Text: "BBB"}},
			},
		),
	}
	out := SerializeConversation(msgs, 2000)
	if !strings.Contains(out, "AAABBB") {
		t.Fatalf("expected concatenated tool result text %q, got:\n%s", "AAABBB", out)
	}
}

func TestSerializeConversationThinking(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant, models.ThinkingContent{Text: "hmm"}),
	}
	out := SerializeConversation(msgs, 2000)
	if !strings.Contains(out, "[Assistant thinking]: hmm") {
		t.Fatalf("missing thinking line: %q", out)
	}
}
