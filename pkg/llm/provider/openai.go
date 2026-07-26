// pkg/llm/provider/openai.go
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// OpenAICompat is the adapter for OpenAI and all OpenAI-compatible providers
// (deepseek, moonshot, openrouter, gemini via its OpenAI endpoint).
type OpenAICompat struct{}

// toolBuffer accumulates a streamed tool call across chunks.
type toolBuffer struct {
	id   string
	name string
	args strings.Builder
}

func (OpenAICompat) Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error) {
	body := map[string]any{
		"model":    req.Model.ID,
		"messages": withSystem(req.SystemPrompt, openAIMessages(req.Messages)),
		"stream":   true,
	}
	if tools := openAITools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if req.Generation.Temperature != 0 {
		body["temperature"] = req.Generation.Temperature
	}
	if req.Generation.MaxTokens != 0 {
		body["max_tokens"] = req.Generation.MaxTokens
	}
	if req.Generation.TopP != 0 {
		body["top_p"] = req.Generation.TopP
	}
	// Thinking: concrete levels pass through; "off" only when the model
	// declares an off encoding; "on" sends nothing (provider default is on).
	if req.Thinking == "off" && req.ThinkingOffEffort != "" {
		body["reasoning_effort"] = req.ThinkingOffEffort
	} else if req.Thinking != "" && req.Thinking != "off" && req.Thinking != "on" {
		body["reasoning_effort"] = req.Thinking
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := ResolveBaseURL(conn) + "/chat/completions"
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
			var chunk openAIChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				usage = chunk.Usage.toLLMUsage()
			} else if len(chunk.Choices) > 0 && chunk.Choices[0].Usage != nil {
				usage = chunk.Choices[0].Usage.toLLMUsage()
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			d := chunk.Choices[0].Delta
			if d.Content != "" {
				textBuf.WriteString(d.Content)
				emit(ctx, out, Event{Kind: KindTextDelta, Delta: d.Content})
			}
			if d.ReasoningContent != "" {
				thinkBuf.WriteString(d.ReasoningContent)
				emit(ctx, out, Event{Kind: KindThinkingDelta, Delta: d.ReasoningContent})
			}
			for _, tc := range d.ToolCalls {
				buf := tools[tc.Index]
				if buf == nil {
					buf = &toolBuffer{}
					tools[tc.Index] = buf
				}
				if tc.ID != "" {
					buf.id = tc.ID
				}
				if tc.Function.Name != "" {
					buf.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					buf.args.WriteString(tc.Function.Arguments)
				}
				emit(ctx, out, Event{Kind: KindToolCallDelta, ToolCallIndex: tc.Index, ArgumentsJSON: tc.Function.Arguments})
			}
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

// withSystem prepends a system message when systemPrompt is non-empty.
func withSystem(systemPrompt string, msgs []map[string]any) []map[string]any {
	if systemPrompt == "" {
		return msgs
	}
	return append([]map[string]any{{"role": "system", "content": systemPrompt}}, msgs...)
}

// finalizeMessage assembles the finished assistant message from accumulated buffers.
func finalizeMessage(thinking, text string, tools map[int]*toolBuffer) models.AgentMessage {
	msg := models.NewAgentMessage(models.RoleAssistant)
	var parts []models.ContentPart
	if thinking != "" {
		parts = append(parts, models.ThinkingContent{Text: thinking})
	}
	if text != "" {
		parts = append(parts, models.TextContent{Text: text})
	}
	idxs := make([]int, 0, len(tools))
	for i := range tools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		buf := tools[i]
		args := map[string]any{}
		raw := buf.args.String()
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				args = map[string]any{"__error__": raw}
			}
		}
		id := buf.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		parts = append(parts, models.ToolCallContent{ID: id, Name: buf.name, Arguments: args})
	}
	msg.Content = parts
	return msg
}

// --- chunk decoding ---

type openAIChunk struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
}

type openAIChoice struct {
	Delta        openAIDelta  `json:"delta"`
	FinishReason string       `json:"finish_reason"`
	Usage        *openAIUsage `json:"usage"`
}

type openAIDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptCacheReadTok  int `json:"prompt_cache_read_tokens"`
	PromptCacheWriteTok int `json:"prompt_cache_write_tokens"`
	// DeepSeek-style prefix-cache fields.
	PromptCacheHitTok  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTok int `json:"prompt_cache_miss_tokens"`
	// OpenAI-style alias for cached prompt tokens.
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

func (u openAIUsage) toLLMUsage() *models.LLMUsage {
	cacheRead := u.PromptCacheReadTok
	if cacheRead == 0 {
		cacheRead = u.PromptCacheHitTok
	}
	if cacheRead == 0 && u.PromptTokensDetails != nil {
		cacheRead = u.PromptTokensDetails.CachedTokens
	}
	return &models.LLMUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: u.PromptCacheWriteTok,
	}
}
