// pkg/llm/provider/anthropic.go
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// Anthropic is the adapter for the Anthropic Messages streaming API. Marks, when
// set by the engine, directs where ephemeral cache_control is applied.
type Anthropic struct {
	Marks CacheMarks
}

// anthropicBlock accumulates one streamed content block by index.
type anthropicBlock struct {
	kind string // text | thinking | tool_use
	id   string // tool_use id
	name string // tool_use name
	text strings.Builder
	args strings.Builder
}

func (a Anthropic) Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error) {
	msgs := anthropicMessages(req.Messages)
	applyMessageCacheMarks(msgs, a.Marks.MessageIdx)
	maxTok := anthropicMaxTokens(req)
	body := map[string]any{
		"model":      req.Model.ID,
		"messages":   msgs,
		"max_tokens": maxTok,
		"stream":     true,
	}
	if sys := anthropicSystem(req); sys != nil {
		if a.Marks.System {
			body["system"] = []map[string]any{{
				"type": "text", "text": req.SystemPrompt,
				"cache_control": ephemeralCacheControl(),
			}}
		} else {
			body["system"] = sys
		}
	}
	if tools := anthropicTools(req.Tools); tools != nil {
		if a.Marks.LastTool {
			tools[len(tools)-1]["cache_control"] = ephemeralCacheControl()
		}
		body["tools"] = tools
	}
	if req.Generation.Temperature != 0 {
		body["temperature"] = req.Generation.Temperature
	}
	if req.Generation.TopP != 0 {
		body["top_p"] = req.Generation.TopP
	}
	// Anthropic has no effort levels: any on-signal enables thinking with a
	// budget derived from the output cap; off is explicit disable.
	if req.Thinking == "off" {
		body["thinking"] = map[string]any{"type": "disabled"}
	} else if req.Thinking != "" {
		budget := maxTok / 2
		if budget < 1024 {
			budget = 1024
		}
		if budget >= maxTok {
			budget = maxTok - 1
		}
		// The API rejects budget_tokens < 1024; omit the field instead of
		// sending a value guaranteed to 400.
		if budget >= 1024 {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := ResolveBaseURL(conn) + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if conn.APIKey != "" {
		httpReq.Header.Set("x-api-key", conn.APIKey)
	}
	for k, v := range conn.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := doStreamRequest(http.DefaultClient, httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		emit(ctx, out, Event{Kind: KindStart})

		blocks := map[int]*anthropicBlock{}
		var usage *models.LLMUsage

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var ev anthropicEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "message_start":
				if ev.Message != nil && ev.Message.Usage != nil {
					usage = applyAnthropicUsage(usage, ev.Message.Usage)
				}
			case "content_block_start":
				b := &anthropicBlock{kind: ev.ContentBlock.Type}
				if ev.ContentBlock.Type == "tool_use" {
					b.id = ev.ContentBlock.ID
					b.name = ev.ContentBlock.Name
				}
				blocks[ev.Index] = b
			case "content_block_delta":
				b := blocks[ev.Index]
				if b == nil {
					b = &anthropicBlock{}
					blocks[ev.Index] = b
				}
				switch ev.Delta.Type {
				case "text_delta":
					b.text.WriteString(ev.Delta.Text)
					emit(ctx, out, Event{Kind: KindTextDelta, Delta: ev.Delta.Text})
				case "thinking_delta":
					b.text.WriteString(ev.Delta.Thinking)
					emit(ctx, out, Event{Kind: KindThinkingDelta, Delta: ev.Delta.Thinking})
				case "input_json_delta":
					b.args.WriteString(ev.Delta.PartialJSON)
					emit(ctx, out, Event{Kind: KindToolCallDelta, ToolCallIndex: ev.Index, ArgumentsJSON: ev.Delta.PartialJSON})
				}
			case "message_delta":
				if ev.Usage != nil {
					usage = applyAnthropicUsage(usage, ev.Usage)
				}
			case "message_stop":
				// stream finished; loop will end at EOF
			}
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: err.Error()}})
			return
		}

		emit(ctx, out, Event{Kind: KindDone,
			Message: finalizeAnthropic(blocks),
			Usage:   usage})
	}()
	return out, nil
}

// finalizeAnthropic assembles the assistant message from accumulated blocks.
func finalizeAnthropic(blocks map[int]*anthropicBlock) models.AgentMessage {
	msg := models.NewAgentMessage(models.RoleAssistant)
	idxs := make([]int, 0, len(blocks))
	for i := range blocks {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	var parts []models.ContentPart
	for _, i := range idxs {
		b := blocks[i]
		switch b.kind {
		case "thinking":
			if b.text.Len() > 0 {
				parts = append(parts, models.ThinkingContent{Text: b.text.String()})
			}
		case "text":
			if b.text.Len() > 0 {
				parts = append(parts, models.TextContent{Text: b.text.String()})
			}
		case "tool_use":
			args := map[string]any{}
			raw := b.args.String()
			if raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					args = map[string]any{"__error__": raw}
				}
			}
			id := b.id
			if id == "" {
				id = fmt.Sprintf("toolu_%d", i)
			}
			parts = append(parts, models.ToolCallContent{ID: id, Name: b.name, Arguments: args})
		}
	}
	msg.Content = parts
	return msg
}

// applyAnthropicUsage merges a partial usage update into the running total.
func applyAnthropicUsage(cur *models.LLMUsage, u *anthropicUsage) *models.LLMUsage {
	if cur == nil {
		cur = &models.LLMUsage{}
	}
	if u.InputTokens != 0 {
		cur.PromptTokens = u.InputTokens
	}
	if u.OutputTokens != 0 {
		cur.CompletionTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens != 0 {
		cur.CacheReadTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != 0 {
		cur.CacheWriteTokens = u.CacheCreationInputTokens
	}
	cur.TotalTokens = cur.PromptTokens + cur.CompletionTokens
	return cur
}

// --- request body helpers ---

// anthropicMessages converts agent messages to Anthropic message blocks. The
// top-level system prompt is handled separately via anthropicSystem, but a
// system-role message can still appear inside the conversation stream (e.g. the
// transient compaction summary produced by the context manager). The Anthropic
// messages array only permits the "user" and "assistant" roles, so such a
// message is emitted as a user turn rather than dropped — the API merges
// consecutive same-role turns, so this never breaks role alternation.
func anthropicMessages(msgs []models.AgentMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case models.RoleUser, models.RoleSystem:
			out = append(out, map[string]any{"role": "user", "content": anthropicUserContent(m.Content)})
		case models.RoleAssistant:
			out = append(out, map[string]any{"role": "assistant", "content": anthropicAssistantContent(m.Content)})
		case models.RoleToolResult:
			out = append(out, map[string]any{"role": "user", "content": anthropicUserContent(m.Content)})
		}
	}
	return out
}

// anthropicUserContent renders user / tool-result parts as Anthropic content blocks.
func anthropicUserContent(parts []models.ContentPart) []map[string]any {
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
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": mime,
						"data":       c.Data,
					},
				})
			}
		case models.ToolResultContent:
			text := ""
			for _, child := range c.Content {
				if t, ok := child.(models.TextContent); ok {
					text += t.Text
				}
			}
			out = append(out, map[string]any{
				"type":        "tool_result",
				"tool_use_id": c.ToolCallID,
				"content":     text,
			})
		}
	}
	return out
}

// anthropicAssistantContent renders assistant parts (text + tool_use blocks).
func anthropicAssistantContent(parts []models.ContentPart) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch c := p.(type) {
		case models.TextContent:
			if c.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": c.Text})
			}
		case models.ThinkingContent:
			if c.Text != "" {
				out = append(out, map[string]any{"type": "thinking", "thinking": c.Text})
			}
		case models.ToolCallContent:
			args := c.Arguments
			if args == nil {
				args = map[string]any{}
			}
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    c.ID,
				"name":  c.Name,
				"input": args,
			})
		}
	}
	return out
}

// anthropicSystem returns the system prompt as a string, or nil when empty.
func anthropicSystem(req models.TurnRequest) any {
	if req.SystemPrompt == "" {
		return nil
	}
	return req.SystemPrompt
}

// anthropicTools converts tool definitions to Anthropic tool schema.
func anthropicTools(tools []models.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	return out
}

// anthropicMaxTokens returns the configured max output tokens, or a 16384
// default. A coding agent routinely needs one turn to both reason and emit a
// tool call (e.g. a file edit); too small a cap truncates the response before
// the tool call is produced, stalling the agent mid-task.
func anthropicMaxTokens(req models.TurnRequest) int {
	if req.Generation.MaxTokens > 0 {
		return req.Generation.MaxTokens
	}
	return 16384
}

// ephemeralCacheControl is the Anthropic cache_control marker for prompt caching.
func ephemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}

// cacheableBlockTypes lists the Anthropic content block types that accept a
// cache_control marker. Thinking blocks notably do not — marking one is a 400.
var cacheableBlockTypes = map[string]bool{
	"text":        true,
	"image":       true,
	"document":    true,
	"tool_use":    true,
	"tool_result": true,
}

// applyMessageCacheMarks adds ephemeral cache_control to the LAST cacheable block
// of each marked message (indices into the built Anthropic messages array).
// Anthropic caches the prefix up to and including the marked block, so marking an
// earlier block would leave the rest of that message uncached. Keying on the
// first *text* block would be worse still: a pure tool_result message — the usual
// tail inside an agent tool loop — has no text block, so the anchor would be
// dropped silently and the whole transcript re-billed as uncached input.
func applyMessageCacheMarks(msgs []map[string]any, idxs []int) {
	for _, i := range idxs {
		if i < 0 || i >= len(msgs) {
			continue
		}
		blocks, ok := msgs[i]["content"].([]map[string]any)
		if !ok {
			continue
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			if t, _ := blocks[j]["type"].(string); cacheableBlockTypes[t] {
				blocks[j]["cache_control"] = ephemeralCacheControl()
				break
			}
		}
	}
}

// --- event decoding ---

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}
