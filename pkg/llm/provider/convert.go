// pkg/llm/provider/convert.go
package provider

import (
	"encoding/json"

	"github.com/lcoder/lcoder/pkg/models"
)

// openAIContent converts content parts to OpenAI message content: a bare string
// for a single text part, else a typed parts array (text / image_url).
func openAIContent(parts []models.ContentPart) any {
	if len(parts) == 1 {
		if t, ok := parts[0].(models.TextContent); ok {
			return t.Text
		}
	}
	out := []map[string]any{}
	for _, p := range parts {
		switch c := p.(type) {
		case models.TextContent:
			if c.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": c.Text})
			}
		case models.ImageContent:
			if c.Data != "" {
				mime := c.MimeType
				if mime == "" {
					mime = "image/jpeg"
				}
				out = append(out, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + mime + ";base64," + c.Data},
				})
			}
		}
	}
	return out
}

// isEmptyContent reports whether an openAIContent result carries no usable parts.
func isEmptyContent(content any) bool {
	switch c := content.(type) {
	case string:
		return c == ""
	case []map[string]any:
		return len(c) == 0
	}
	return false
}

// openAIMessages converts agent messages to OpenAI chat messages, dropping any
// message that produces no representable content.
func openAIMessages(msgs []models.AgentMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case models.RoleSystem, models.RoleUser:
			content := openAIContent(m.Content)
			if isEmptyContent(content) {
				continue
			}
			role := "system"
			if m.Role == models.RoleUser {
				role = "user"
			}
			out = append(out, map[string]any{"role": role, "content": content})
		case models.RoleAssistant:
			var assistantContent []map[string]any
			var toolCalls []map[string]any
			for _, p := range m.Content {
				switch c := p.(type) {
				case models.TextContent:
					if c.Text != "" {
						assistantContent = append(assistantContent, map[string]any{"type": "text", "text": c.Text})
					}
				case models.ToolCallContent:
					args, _ := json.Marshal(c.Arguments)
					if c.Arguments == nil {
						args = []byte("{}")
					}
					toolCalls = append(toolCalls, map[string]any{
						"id":   c.ID,
						"type": "function",
						"function": map[string]any{
							"name":      c.Name,
							"arguments": string(args),
						},
					})
				}
			}
			msg := map[string]any{"role": "assistant"}
			if len(assistantContent) > 0 {
				msg["content"] = assistantContent
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			out = append(out, msg)
		case models.RoleToolResult:
			var result *models.ToolResultContent
			for _, p := range m.Content {
				if r, ok := p.(models.ToolResultContent); ok {
					result = &r
					break
				}
			}
			if result == nil {
				continue
			}
			text := ""
			for _, child := range result.Content {
				if t, ok := child.(models.TextContent); ok {
					text += t.Text
				}
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": result.ToolCallID,
				"content":      text,
			})
		}
	}
	return out
}

// openAITools converts tool definitions to OpenAI function tools.
func openAITools(tools []models.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return out
}
