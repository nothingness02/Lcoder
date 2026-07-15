package tui

import (
	"time"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tui/components"
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
		m.compacting = false
		if e.Message.Role == models.RoleAssistant {
			m.streaming = true
			m.streamLive = ""
			m.streamMsgID = e.Message.ID
			m.appendBlock(block{kind: components.BlockAssistant, id: e.Message.ID, raw: ""})
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
			// The provider may finalize with a message object whose ID differs from
			// the partial we streamed. Patch the in-flight block using the streaming
			// ID we recorded so we don't append a duplicate assistant paragraph.
			id := m.streamMsgID
			if id == "" {
				id = e.Message.ID
			}
			m.commitAssistant(id, final, e.Message.Thinking(), usagePtr(e.Message))
			m.streaming = false
			m.streamLive = ""
			m.streamMsgID = ""
		}

	case events.TaskListUpdatedEvent:
		m.tasks = e.Tasks
		m.updateSizes()

	case events.ToolExecutionStartEvent:
		m.appendBlock(block{
			kind:        components.BlockTool,
			id:          e.ToolCallID,
			toolName:    e.ToolName,
			toolArgs:    FormatArgs(e.Args),
			toolStart:   time.Now(),
			toolRunning: true,
		})

	case events.ToolExecutionEndEvent:
		m.finishTool(e.ToolCallID, e.ToolName, e.Result, e.IsError)
		m.turnTools = append(m.turnTools, toolResultEntry{
			name:    e.ToolName,
			isError: e.IsError,
			content: e.Result.Text(),
		})

	case events.TurnEndEvent:
		if len(m.turnTools) > 0 {
			m.addSystem(formatToolSummary(m.turnTools))
			m.turnTools = m.turnTools[:0]
		}

	case events.AgentEndEvent:
		m.completedTurns++
		m.compacting = false

	case events.CompactionStartedEvent:
		m.compacting = true

	case events.CompactionCommittedEvent:
		m.compacting = false
		m.addSystem("↧ 已压缩早前对话以节省 token(原始记录已合并为摘要)")

	case events.ErrorEvent:
		m.compacting = false
		m.errMsg = e.Message
		m.addSystem(styleError().Render("error: " + e.Message))
	}
}

// patchAssistant overwrites the raw content of the in-flight assistant block.
func (m *Model) patchAssistant(content string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockAssistant && m.blocks[i].id == m.streamMsgID {
			m.blocks[i].raw = content
			if ac, ok := m.components[i].(*components.AssistantComponent); ok {
				ac.SetContent(content)
			} else {
				m.components[i] = toComponent(m.blocks[i])
			}
			m.rebuildViewport()
			return
		}
	}
}

// commitAssistant finalizes the assistant block with content, thinking, and usage.
func (m *Model) commitAssistant(id, content, thinking string, usage *blockUsage) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockAssistant && m.blocks[i].id == id {
			m.blocks[i].raw = content
			m.blocks[i].thinking = thinking
			m.blocks[i].usage = usage
			if usage != nil {
				m.totalCost += usage.cost
			}
			m.components[i] = toComponent(m.blocks[i])
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: components.BlockAssistant, id: id, raw: content, thinking: thinking, usage: usage})
	if usage != nil {
		m.totalCost += usage.cost
	}
}

// finishTool patches the tool block identified by id with its result.
func (m *Model) finishTool(id, name string, result models.ToolExecutionResult, isError bool) {
	text := result.Text()
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockTool && m.blocks[i].id == id {
			m.blocks[i].toolResult = text
			m.blocks[i].toolErr = isError
			m.blocks[i].toolRunning = false
			if !m.blocks[i].toolStart.IsZero() {
				m.blocks[i].elapsed = time.Since(m.blocks[i].toolStart)
			}
			m.components[i] = toComponent(m.blocks[i])
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: components.BlockTool, id: id, toolName: name, toolResult: text, toolErr: isError})
}

// blocksFromMessages rebuilds the block history from a stored conversation.
func blocksFromMessages(msgs []models.AgentMessage) []block {
	var out []block
	for _, msg := range msgs {
		switch msg.Role {
		case models.RoleUser:
			out = append(out, block{kind: components.BlockUser, id: msg.ID, raw: msg.Text()})
		case models.RoleAssistant:
			out = append(out, block{
				kind:     components.BlockAssistant,
				id:       msg.ID,
				raw:      msg.Text(),
				thinking: msg.Thinking(),
				usage:    usagePtr(msg),
			})
			for _, tc := range msg.ToolCalls() {
				out = append(out, block{
					kind:     components.BlockTool,
					id:       tc.ID,
					toolName: tc.Name,
					toolArgs: FormatArgs(tc.Arguments),
				})
			}
		case models.RoleToolResult:
			out = append(out, block{kind: components.BlockTool, id: msg.ID, toolResult: msg.Text()})
		case models.RoleSystem:
			out = append(out, block{kind: components.BlockSystem, raw: msg.Text()})
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

// mcpServers maps an mcp.Registry to the extensions panel's server rows.
func mcpServers(reg *mcp.Registry) []mcp.ServerStatus {
	if reg == nil {
		return nil
	}
	return reg.Servers()
}
