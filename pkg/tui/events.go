package tui

import (
	"strings"
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
			m.streamLiveThinking = ""
			m.streamMsgID = e.Message.ID
			m.appendBlock(block{kind: components.BlockAssistant, id: e.Message.ID, raw: ""})
		}

	case events.MessageUpdateEvent:
		if !m.streaming {
			break
		}
		if e.IsThinking {
			m.streamLiveThinking += e.Delta
			m.patchThinking(m.streamLiveThinking)
		} else {
			m.streamLive += e.Delta
			m.patchAssistant(m.streamLive)
		}

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
			m.streamLiveThinking = ""
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
		m.updateContextStats()

	case events.CompactionStartedEvent:
		m.compacting = true

	case events.CompactionCommittedEvent:
		m.compacting = false
		m.addSystem(formatCompactResult(e.TokensBefore, e.TokensAfter, e.Summary))
		m.updateContextStats()

	case events.ErrorEvent:
		m.compacting = false
		// Errors surface in the fixed region above the composer (see
		// bottomRegion), not as a transcript block, so they don't get buried in
		// the scrollback and clear on the next prompt.
		m.errMsg = e.Message

	case events.SubagentActivityEvent:
		m.handleSubagentActivity(e)

	case events.BackgroundNoticeEvent:
		m.addSystem(e.Text)
	}
}

// handleSubagentActivity routes one mirrored child-agent activity event into
// the tool block of the parent's subagent call, rendering it as nested
// activity (kimi-code's nested subagent display). Events whose parent tool
// call is not on screen are dropped silently.
func (m *Model) handleSubagentActivity(e events.SubagentActivityEvent) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.kind != components.BlockTool || b.id != e.ParentToolCallID {
			continue
		}
		child := m.subagentChildFor(b, e.AgentID, e.Profile)
		switch e.Kind {
		case events.SubagentStarted:
			b.subagentLive = true
		case events.SubagentText:
			b.subagentTail += e.Text
		case events.SubagentToolStart:
			m.flushSubagentTail(b)
			b.subagentLines = append(b.subagentLines, "→ "+e.Text)
			child.tools++
		case events.SubagentToolEnd:
			m.flushSubagentTail(b)
			b.subagentLines = append(b.subagentLines, "✓ "+e.Text)
		case events.SubagentTurn:
			m.flushSubagentTail(b)
			b.subagentLines = append(b.subagentLines, "· turn "+e.Text)
		case events.SubagentFailed:
			m.flushSubagentTail(b)
			b.subagentLines = append(b.subagentLines, "✗ "+e.Text)
			settleSubagentChild(child, "failed")
		case events.SubagentCompleted:
			m.flushSubagentTail(b)
			status := e.Text
			if status == "" {
				status = "completed"
			}
			settleSubagentChild(child, status)
			if allSubagentChildrenSettled(b) {
				b.subagentLive = false
			}
		}
		m.components[i] = toComponent(m.blocks[i])
		m.rebuildViewport()
		return
	}
}

// subagentChildFor returns (creating if needed) the per-child state row for
// an agent id within a tool block.
func (m *Model) subagentChildFor(b *block, agentID, profile string) *subagentChild {
	if b.subagentChildren == nil {
		b.subagentChildren = make(map[string]*subagentChild)
	}
	child, ok := b.subagentChildren[agentID]
	if !ok {
		child = &subagentChild{profile: profile, status: "running", started: time.Now()}
		b.subagentChildren[agentID] = child
		b.subagentOrder = append(b.subagentOrder, agentID)
	}
	return child
}

// settleSubagentChild marks a child finished with a final status.
func settleSubagentChild(child *subagentChild, status string) {
	child.status = status
	child.elapsed = time.Since(child.started)
}

// allSubagentChildrenSettled reports whether every child in the block has a
// final status.
func allSubagentChildrenSettled(b *block) bool {
	for _, child := range b.subagentChildren {
		if child.status == "running" {
			return false
		}
	}
	return true
}

// flushSubagentTail moves the in-flight text tail into a completed line.
func (m *Model) flushSubagentTail(b *block) {
	if b.subagentTail == "" {
		return
	}
	b.subagentLines = append(b.subagentLines, b.subagentTail)
	b.subagentTail = ""
}

// streamLiveMaxBytes caps the in-flight assistant text that is re-rendered as
// markdown on every delta. The full content is still accumulated in streamLive
// (and used as the commit fallback); only the rendered tail is clipped so each
// frame stays O(maxBytes) during long streams. Mirrors Kocoro's boundStreamTail.
const streamLiveMaxBytes = 32768

// boundStreamTail returns the tail of s capped at maxBytes, cut at the first
// line boundary so the clip does not start mid-line. Strings at or below
// maxBytes are returned unchanged.
func boundStreamTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	tail := s[len(s)-maxBytes:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return tail
}

// patchAssistant overwrites the raw content of the in-flight assistant block.
// The rendered input is capped to streamLiveMaxBytes (see boundStreamTail); the
// full text lives on in streamLive and is restored when commitAssistant runs.
func (m *Model) patchAssistant(content string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockAssistant && m.blocks[i].id == m.streamMsgID {
			rendered := boundStreamTail(content, streamLiveMaxBytes)
			m.blocks[i].raw = rendered
			if ac, ok := m.components[i].(*components.AssistantComponent); ok {
				ac.SetContent(rendered)
			} else {
				m.components[i] = toComponent(m.blocks[i])
			}
			m.rebuildViewport()
			return
		}
	}
}

// patchThinking overwrites the thinking trace of the in-flight assistant block.
func (m *Model) patchThinking(thinking string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockAssistant && m.blocks[i].id == m.streamMsgID {
			m.blocks[i].thinking = thinking
			if ac, ok := m.components[i].(*components.AssistantComponent); ok {
				ac.SetThinking(thinking)
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
			if ec, ok := m.components[i].(components.ExpandableComponent); ok {
				m.blocks[i].expanded = ec.Expanded()
			}
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
			if ec, ok := m.components[i].(components.ExpandableComponent); ok {
				m.blocks[i].expanded = ec.Expanded()
			}
			m.blocks[i].toolResult = text
			m.blocks[i].toolErr = isError
			m.blocks[i].toolRunning = false
			m.blocks[i].toolChip = chipForTool(name, result)
			if !m.blocks[i].toolStart.IsZero() {
				m.blocks[i].elapsed = time.Since(m.blocks[i].toolStart)
			}
			m.components[i] = toComponent(m.blocks[i])
			m.rebuildViewport()
			return
		}
	}
	m.appendBlock(block{kind: components.BlockTool, id: id, toolName: name, toolResult: text, toolErr: isError, toolChip: chipForTool(name, result)})
}

// blocksFromMessages rebuilds the block history from a stored conversation.
func blocksFromMessages(msgs []models.AgentMessage) []block {
	var out []block
	// Map tool-call ID to the index of its BlockTool so a later RoleToolResult
	// can be merged into the same visual row.
	toolIdx := make(map[string]int)

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
				toolIdx[tc.ID] = len(out)
				out = append(out, block{
					kind:     components.BlockTool,
					id:       tc.ID,
					toolName: tc.Name,
					toolArgs: FormatArgs(tc.Arguments),
				})
			}
		case models.RoleToolResult:
			toolCallID, name, resultText, isError := extractToolResult(msg)
			if idx, ok := toolIdx[toolCallID]; ok && toolCallID != "" {
				out[idx].toolResult = resultText
				out[idx].toolErr = isError
				if out[idx].toolName == "" {
					out[idx].toolName = name
				}
				continue
			}
			out = append(out, block{kind: components.BlockTool, id: msg.ID, toolName: name, toolResult: resultText, toolErr: isError})
		case models.RoleSystem:
			out = append(out, block{kind: components.BlockSystem, raw: msg.Text()})
		}
	}
	return out
}

// extractToolResult pulls the call ID, name, text, and error flag from a tool
// result message. It prefers the structured ToolResultContent envelope, falling
// back to plain text content when the provider stored a simpler message.
func extractToolResult(msg models.AgentMessage) (toolCallID, name, resultText string, isError bool) {
	for _, part := range msg.Content {
		if tr, ok := part.(models.ToolResultContent); ok {
			toolCallID = tr.ToolCallID
			name = tr.Name
			isError = tr.IsError
			resultText = tr.Text()
			break
		}
	}
	if resultText == "" {
		resultText = msg.Text()
	}
	return
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
