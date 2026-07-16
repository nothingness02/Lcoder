package desktop

import "github.com/lcoder/lcoder/pkg/models"

// MessageToUI converts a single internal message to a UI message.
func MessageToUI(m models.AgentMessage) UIMessage {
	ui := UIMessage{ID: m.ID, Role: string(m.Role), Timestamp: m.Timestamp}
	for _, p := range m.Content {
		switch c := p.(type) {
		case models.TextContent:
			ui.Text += c.Text
		case models.ThinkingContent:
			ui.Thinking += c.Text
		case models.ToolCallContent:
			ui.ToolCalls = append(ui.ToolCalls, UIToolCall{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: c.Arguments,
			})
		case models.ToolResultContent:
			ui.ToolResult = &UIToolResult{
				ToolCallID: c.ToolCallID,
				Name:       c.Name,
				Output:     c.Text(),
				IsError:    c.IsError,
			}
		}
	}
	return ui
}

// MessagesToUI converts a conversation to UI messages, merging tool results
// into their matching assistant tool-call block.
func MessagesToUI(msgs []models.AgentMessage) []UIMessage {
	// First pass: collect all tool results by call ID, regardless of order.
	results := make(map[string]*UIToolResult)
	for _, m := range msgs {
		ui := MessageToUI(m)
		if ui.Role == string(models.RoleToolResult) && ui.ToolResult != nil {
			results[ui.ToolResult.ToolCallID] = ui.ToolResult
		}
	}

	// Second pass: build output, skipping tool-result messages and attaching
	// results to their matching tool calls.
	out := make([]UIMessage, 0, len(msgs))
	for _, m := range msgs {
		ui := MessageToUI(m)
		if ui.Role == string(models.RoleToolResult) {
			continue
		}
		for i := range ui.ToolCalls {
			if r, ok := results[ui.ToolCalls[i].ID]; ok {
				ui.ToolCalls[i].Result = r
			}
		}
		out = append(out, ui)
	}
	return out
}
