// pkg/llm/provider/openai_responses.go
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// OpenAIResponses is the adapter for the OpenAI Responses API (/responses),
// the only way to reach codex-class models. Tools are flattened and tool
// calls correlate by call_id, unlike chat completions.
type OpenAIResponses struct{}

func (OpenAIResponses) Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error) {
	body := map[string]any{
		"model":  req.Model.ID,
		"input":  responsesInput(req.Messages),
		"stream": true,
	}
	if req.SystemPrompt != "" {
		body["instructions"] = req.SystemPrompt
	}
	if tools := responsesTools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if req.Generation.Temperature != 0 {
		body["temperature"] = req.Generation.Temperature
	}
	if req.Generation.MaxTokens != 0 {
		body["max_output_tokens"] = req.Generation.MaxTokens
	}
	if req.Generation.TopP != 0 {
		body["top_p"] = req.Generation.TopP
	}
	if req.Thinking == "off" && req.ThinkingOffEffort != "" {
		body["reasoning"] = map[string]any{"effort": req.ThinkingOffEffort}
	} else if req.Thinking != "" && req.Thinking != "off" && req.Thinking != "on" {
		body["reasoning"] = map[string]any{"effort": req.Thinking}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := ResolveBaseURL(conn) + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conn.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+conn.APIKey)
	}
	for k, v := range conn.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			emit(ctx, out, Event{Kind: KindError, Err: classifyHTTP(resp.StatusCode, data)})
			return
		}

		emit(ctx, out, Event{Kind: KindStart})

		var textBuf, thinkBuf strings.Builder
		tools := map[int]*toolBuffer{}
		toolIdxByItemID := map[string]int{}
		var usage *models.LLMUsage

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				break
			}
			var ev responsesEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "response.output_text.delta":
				textBuf.WriteString(ev.Delta)
				emit(ctx, out, Event{Kind: KindTextDelta, Delta: ev.Delta})
			case "response.reasoning_summary_text.delta":
				thinkBuf.WriteString(ev.Delta)
				emit(ctx, out, Event{Kind: KindThinkingDelta, Delta: ev.Delta})
			case "response.output_item.done":
				if ev.Item.Type == "function_call" {
					// Correlate by output-item id: deltas key on item_id, which
					// equals item.id (fc_...), not call_id (call_...).
					idx, ok := toolIdxByItemID[ev.Item.ID]
					if !ok {
						idx = len(toolIdxByItemID)
						toolIdxByItemID[ev.Item.ID] = idx
						tools[idx] = &toolBuffer{}
					}
					buf := tools[idx]
					buf.id = ev.Item.CallID
					buf.name = ev.Item.Name
					if buf.args.Len() == 0 && ev.Item.Arguments != "" {
						buf.args.WriteString(ev.Item.Arguments)
					}
				}
			case "response.function_call_arguments.delta":
				idx, ok := toolIdxByItemID[ev.ItemID]
				if !ok {
					idx = len(toolIdxByItemID)
					toolIdxByItemID[ev.ItemID] = idx
					tools[idx] = &toolBuffer{}
				}
				tools[idx].args.WriteString(ev.Delta)
				emit(ctx, out, Event{Kind: KindToolCallDelta, ToolCallIndex: idx, ArgumentsJSON: ev.Delta})
			case "response.completed":
				if ev.Response.Usage != nil {
					usage = ev.Response.Usage.toLLMUsage()
				}
			case "response.failed", "error":
				msg := "responses stream failed"
				if ev.Response.Error != nil && ev.Response.Error.Message != "" {
					msg = ev.Response.Error.Message
				} else if ev.Message != "" {
					msg = ev.Message
				}
				emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: msg}})
				return
			}
			// Unknown event types are skipped (forward compatible).
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: err.Error()}})
			return
		}

		emit(ctx, out, Event{Kind: KindDone,
			Message: finalizeMessage(thinkBuf.String(), textBuf.String(), tools),
			Usage:   usage})
	}()
	return out, nil
}

// responsesInput converts agent messages to Responses input items.
func responsesInput(msgs []models.AgentMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case models.RoleSystem, models.RoleUser:
			var parts []map[string]any
			for _, p := range m.Content {
				switch c := p.(type) {
				case models.TextContent:
					if c.Text != "" {
						parts = append(parts, map[string]any{"type": "input_text", "text": c.Text})
					}
				case models.ImageContent:
					if c.Data != "" {
						mime := c.MimeType
						if mime == "" {
							mime = "image/jpeg"
						}
						parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + mime + ";base64," + c.Data})
					}
				}
			}
			if len(parts) > 0 {
				out = append(out, map[string]any{"role": "user", "content": parts})
			}
		case models.RoleAssistant:
			var textParts []map[string]any
			for _, p := range m.Content {
				switch c := p.(type) {
				case models.TextContent:
					if c.Text != "" {
						textParts = append(textParts, map[string]any{"type": "output_text", "text": c.Text})
					}
				case models.ToolCallContent:
					args, _ := json.Marshal(c.Arguments)
					if c.Arguments == nil {
						args = []byte("{}")
					}
					out = append(out, map[string]any{
						"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": string(args),
					})
				}
			}
			if len(textParts) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": textParts})
			}
		case models.RoleToolResult:
			for _, p := range m.Content {
				if r, ok := p.(models.ToolResultContent); ok {
					text := ""
					for _, child := range r.Content {
						if t, ok := child.(models.TextContent); ok {
							text += t.Text
						}
					}
					out = append(out, map[string]any{
						"type": "function_call_output", "call_id": r.ToolCallID, "output": text,
					})
				}
			}
		}
	}
	return out
}

// responsesTools converts tool definitions to the flattened Responses shape.
func responsesTools(tools []models.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters,
		})
	}
	return out
}

// --- event decoding ---

type responsesEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	// function_call_arguments.delta carries item_id; output_item.done carries item.
	// Top-level error events carry message.
	ItemID  string `json:"item_id"`
	Message string `json:"message"`
	Item    struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response struct {
		Usage *responsesUsage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
}

func (u responsesUsage) toLLMUsage() *models.LLMUsage {
	cacheRead := 0
	if u.InputTokensDetails != nil {
		cacheRead = u.InputTokensDetails.CachedTokens
	}
	return &models.LLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  cacheRead,
	}
}
