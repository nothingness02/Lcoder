package tui

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/models"
)

// handleEvent applies one agent event to the model's block history.
func (m *Model) handleEvent(ev events.Event) {
	switch e := ev.(type) {
	case events.AgentStartEvent:
		m.streaming = false
		m.streamLive = ""
		m.streamMsgID = ""
		m.turnTools = m.turnTools[:0]

	case events.MessageStartEvent:
		if e.Message.Role == models.RoleAssistant {
			m.streaming = true
			m.streamLive = ""
			m.streamMsgID = e.Message.ID
			m.appendBlock(block{kind: blockAssistant, id: e.Message.ID, raw: ""})
		}

	case events.MessageUpdateEvent:
		if !m.streaming {
			break
		}
		m.streamLive += e.Delta
		m.patchAssistant(m.streamLive)

	case events.MessageEndEvent:
		if e.Message.Role == models.RoleAssistant {
			final := e.Message.Text()
			if final == "" {
				final = m.streamLive
			}
			m.commitAssistant(e.Message.ID, final, e.Message.Thinking(), usagePtr(e.Message))
			m.streaming = false
			m.streamLive = ""
			m.streamMsgID = ""
		}

	case events.TaskListUpdatedEvent:
		m.tasks = e.Tasks
		m.updateSizes()

	case events.ToolExecutionStartEvent:
		m.appendBlock(block{
			kind:     blockTool,
			id:       e.ToolCallID,
			toolName: e.ToolName,
			toolArgs: FormatArgs(e.Args),
		})

	case events.ToolExecutionEndEvent:
		m.finishTool(e.ToolCallID, e.ToolName, e.Result, e.IsError)
		m.turnTools = append(m.turnTools, toolResultEntry{
			name:    e.ToolName,
			isError: e.IsError,
			content: toolResultText(e.Result),
		})

	case events.AgentEndEvent:
		m.completedTurns++

	case events.CompactionCommittedEvent:
		m.addSystem("↧ 已压缩早前对话以节省 token(原始记录已合并为摘要)")

	case events.ErrorEvent:
		m.errMsg = e.Message
		m.addSystem(styleError().Render("error: " + e.Message))
	}
}

// patchAssistant overwrites the raw content of the in-flight assistant block.
func (m *Model) patchAssistant(content string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockAssistant && m.blocks[i].id == m.streamMsgID {
			m.blocks[i].raw = content
			m.rebuildViewport()
			return
		}
	}
}

// commitAssistant finalizes the assistant block with content, thinking, and usage.
func (m *Model) commitAssistant(id, content, thinking string, usage *blockUsage) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockAssistant && m.blocks[i].id == id {
			m.blocks[i].raw = content
			m.blocks[i].thinking = thinking
			m.blocks[i].usage = usage
			if usage != nil {
				m.totalCost += usage.cost
			}
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: blockAssistant, id: id, raw: content, thinking: thinking, usage: usage})
	if usage != nil {
		m.totalCost += usage.cost
	}
}

// finishTool patches the tool block identified by id with its result.
func (m *Model) finishTool(id, name string, result models.ToolExecutionResult, isError bool) {
	text := toolResultText(result)
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool && m.blocks[i].id == id {
			m.blocks[i].raw = text
			m.blocks[i].toolErr = isError
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: blockTool, id: id, toolName: name, raw: text, toolErr: isError})
}

// blocksFromMessages rebuilds the block history from a stored conversation.
func blocksFromMessages(msgs []models.AgentMessage) []block {
	var out []block
	for _, msg := range msgs {
		switch msg.Role {
		case models.RoleUser:
			out = append(out, block{kind: blockUser, id: msg.ID, raw: msg.Text()})
		case models.RoleAssistant:
			out = append(out, block{
				kind:     blockAssistant,
				id:       msg.ID,
				raw:      msg.Text(),
				thinking: msg.Thinking(),
				usage:    usagePtr(msg),
			})
			for _, tc := range msg.ToolCalls() {
				out = append(out, block{
					kind:     blockTool,
					id:       tc.ID,
					toolName: tc.Name,
					toolArgs: FormatArgs(tc.Arguments),
				})
			}
		case models.RoleToolResult:
			out = append(out, block{kind: blockTool, id: msg.ID, raw: msg.Text()})
		case models.RoleSystem:
			out = append(out, block{kind: blockSystem, raw: msg.Text()})
		}
	}
	return out
}

// --- Relocated helpers (VERIFIED against pkg/models/message.go + old model.go) ---

// extractUsage pulls LLMUsage from the message metadata.
func extractUsage(msg models.AgentMessage) (models.LLMUsage, bool) {
	if msg.Metadata == nil {
		return models.LLMUsage{}, false
	}
	v, ok := msg.Metadata["usage"]
	if !ok {
		return models.LLMUsage{}, false
	}
	u, ok := v.(models.LLMUsage)
	return u, ok
}

// usagePtr adapts extractUsage into the *blockUsage the block renderer wants.
func usagePtr(msg models.AgentMessage) *blockUsage {
	u, ok := extractUsage(msg)
	if !ok {
		return nil
	}
	return &blockUsage{
		inputTokens:  u.PromptTokens,
		outputTokens: u.CompletionTokens,
		totalTokens:  u.TotalTokens,
		cost:         u.TotalCost,
	}
}

// toolResultText renders a ToolExecutionResult to plain text.
func toolResultText(result models.ToolExecutionResult) string {
	var out string
	for _, part := range result.Content {
		if text, ok := part.(models.TextContent); ok {
			out += text.Text
		}
	}
	if len(out) > 200 {
		out = out[:197] + "..."
	}
	return out
}

// mcpServers maps an mcp.Registry to the extensions panel's server rows.
func mcpServers(reg *mcp.Registry) []mcp.ServerStatus {
	if reg == nil {
		return nil
	}
	return reg.Servers()
}

// formatTokenCount renders a token count compactly (1234 -> 1.2k).
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
