package compaction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// SerializeConversation renders messages as plain text for the summarizer,
// mirroring pi's serializeConversation: explicit role labels prevent the model
// from treating the input as a conversation to continue. Tool result text is
// truncated to maxToolResultChars so a single huge read/bash output cannot
// overflow the summarization request itself.
func SerializeConversation(msgs []models.AgentMessage, maxToolResultChars int) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case models.RoleUser:
			fmt.Fprintf(&b, "[User]: %s\n", m.Text())
		case models.RoleAssistant:
			if thinking := m.Thinking(); thinking != "" {
				fmt.Fprintf(&b, "[Assistant thinking]: %s\n", thinking)
			}
			if text := m.Text(); text != "" {
				fmt.Fprintf(&b, "[Assistant]: %s\n", text)
			}
			if calls := m.ToolCalls(); len(calls) > 0 {
				fmt.Fprintf(&b, "[Assistant tool calls]: %s\n", renderToolCalls(calls))
			}
		case models.RoleToolResult:
			text := toolResultText(m)
			if maxToolResultChars > 0 && len(text) > maxToolResultChars {
				text = text[:maxToolResultChars] + fmt.Sprintf("\n...[truncated %d chars]", len(text)-maxToolResultChars)
			}
			fmt.Fprintf(&b, "[Tool result]: %s\n", text)
		default:
			if text := m.Text(); text != "" {
				fmt.Fprintf(&b, "[%s]: %s\n", m.Role, text)
			}
		}
	}
	return b.String()
}

// toolResultText concatenates the text of all ToolResultContent parts in a
// tool_result message (AgentMessage.Text only sees top-level TextContent).
func toolResultText(m models.AgentMessage) string {
	var out string
	for _, part := range m.Content {
		if tr, ok := part.(models.ToolResultContent); ok {
			out += tr.Text()
		}
	}
	return out
}

// renderToolCalls renders calls as name(arg="value", ...) joined by "; ".
// Argument keys are sorted for deterministic output.
func renderToolCalls(calls []models.ToolCallContent) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		keys := make([]string, 0, len(c.Arguments))
		for k := range c.Arguments {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		args := make([]string, 0, len(keys))
		for _, k := range keys {
			args = append(args, fmt.Sprintf("%s=%q", k, fmt.Sprint(c.Arguments[k])))
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", c.Name, strings.Join(args, ", ")))
	}
	return strings.Join(parts, "; ")
}
