package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MessageRole defines the role of an agent message.
type MessageRole string

const (
	RoleUser         MessageRole = "user"
	RoleAssistant    MessageRole = "assistant"
	RoleToolResult   MessageRole = "tool_result"
	RoleSystem       MessageRole = "system"
	RoleNotification MessageRole = "notification"
	// RoleCustom marks extension-owned custom entries. They persist in the
	// session file but never enter model context.
	RoleCustom MessageRole = "custom"
)

// AgentMessage is the internal message representation used throughout the agent.
// It can hold LLM-visible content as well as UI-only or custom metadata.
type AgentMessage struct {
	ID        string         `json:"id"`
	ParentID  *string        `json:"parent_id,omitempty"`
	Role      MessageRole    `json:"role"`
	Content   []ContentPart  `json:"content"`
	Timestamp int64          `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// WithMetadata returns a shallow copy of the message with additional metadata.
func (m AgentMessage) WithMetadata(key string, value any) AgentMessage {
	if m.Metadata == nil {
		m.Metadata = make(map[string]any)
	}
	m.Metadata[key] = value
	return m
}

// NewAgentMessage creates a message with a generated ID and current timestamp.
func NewAgentMessage(role MessageRole, content ...ContentPart) AgentMessage {
	return AgentMessage{
		ID:        uuid.New().String()[:12],
		Role:      role,
		Content:   content,
		Timestamp: time.Now().UnixMilli(),
		Metadata:  make(map[string]any),
	}
}

// UserMessage is a convenience constructor for user text messages.
func UserMessage(text string) AgentMessage {
	return NewAgentMessage(RoleUser, TextContent{Text: text})
}

// AssistantMessage is a convenience constructor for assistant text messages.
func AssistantMessage(text string) AgentMessage {
	return NewAgentMessage(RoleAssistant, ContentPart(TextContent{Text: text}))
}

// Text returns the concatenated text of all text content parts.
func (m AgentMessage) Text() string {
	var out string
	for _, part := range m.Content {
		if t, ok := part.(TextContent); ok {
			out += t.Text
		}
	}
	return out
}

// Thinking returns the concatenated thinking/reasoning text.
func (m AgentMessage) Thinking() string {
	var out string
	for _, part := range m.Content {
		if t, ok := part.(ThinkingContent); ok {
			out += t.Text
		}
	}
	return out
}

// ToolCalls extracts tool call content parts from an assistant message.
func (m AgentMessage) ToolCalls() []ToolCallContent {
	var calls []ToolCallContent
	for _, part := range m.Content {
		if tc, ok := part.(ToolCallContent); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// MarshalJSON implements custom JSON serialization for AgentMessage content.
func (m AgentMessage) MarshalJSON() ([]byte, error) {
	type alias AgentMessage
	return json.Marshal(
		&struct {
			Content []contentPartEnvelope `json:"content"`
			*alias
		}{
			Content: wrapContentParts(m.Content),
			alias:   (*alias)(&m),
		})
}

// UnmarshalJSON implements custom JSON deserialization for AgentMessage content.
func (m *AgentMessage) UnmarshalJSON(data []byte) error {
	type alias AgentMessage
	aux := &struct {
		Content []contentPartEnvelope `json:"content"`
		*alias
	}{
		alias: (*alias)(m),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	m.Content = unwrapContentParts(aux.Content)
	return nil
}

// contentPartEnvelope is used for polymorphic JSON serialization.
type contentPartEnvelope struct {
	Type       string                `json:"type"`
	Text       string                `json:"text,omitempty"`
	Data       string                `json:"data,omitempty"`
	MimeType   string                `json:"mime_type,omitempty"`
	ID         string                `json:"id,omitempty"`
	Name       string                `json:"name,omitempty"`
	Arguments  map[string]any        `json:"arguments,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	Content    []contentPartEnvelope `json:"content,omitempty"`
	IsError    bool                  `json:"is_error,omitempty"`
	Details    map[string]any        `json:"details,omitempty"`
}

func wrapContentParts(parts []ContentPart) []contentPartEnvelope {
	var envs []contentPartEnvelope
	for _, part := range parts {
		envs = append(envs, wrapContentPart(part))
	}
	return envs
}

func wrapContentPart(part ContentPart) contentPartEnvelope {
	switch p := part.(type) {
	case TextContent:
		return contentPartEnvelope{Type: "text", Text: p.Text}
	case ThinkingContent:
		return contentPartEnvelope{Type: "thinking", Text: p.Text}
	case ImageContent:
		return contentPartEnvelope{Type: "image", Data: p.Data, MimeType: p.MimeType}
	case ToolCallContent:
		return contentPartEnvelope{Type: "tool_call", ID: p.ID, Name: p.Name, Arguments: p.Arguments}
	case ToolResultContent:
		return contentPartEnvelope{
			Type:       "tool_result",
			ToolCallID: p.ToolCallID,
			Name:       p.Name,
			Content:    wrapContentParts(p.Content),
			IsError:    p.IsError,
			Details:    p.Details,
		}
	default:
		return contentPartEnvelope{Type: "unknown"}
	}
}

func unwrapContentParts(envs []contentPartEnvelope) []ContentPart {
	var parts []ContentPart
	for _, env := range envs {
		part := unwrapContentPart(env)
		if part != nil {
			parts = append(parts, part)
		}
	}
	return parts
}

func unwrapContentPart(env contentPartEnvelope) ContentPart {
	switch env.Type {
	case "text":
		return TextContent{Text: env.Text}
	case "thinking":
		return ThinkingContent{Text: env.Text}
	case "image":
		return ImageContent{Data: env.Data, MimeType: env.MimeType}
	case "tool_call":
		return ToolCallContent{ID: env.ID, Name: env.Name, Arguments: env.Arguments}
	case "tool_result":
		return ToolResultContent{
			ToolCallID: env.ToolCallID,
			Name:       env.Name,
			Content:    unwrapContentParts(env.Content),
			IsError:    env.IsError,
			Details:    env.Details,
		}
	default:
		// Ignore unknown types during deserialization.
		return nil
	}
}

// ContentPart is a polymorphic content unit inside an AgentMessage.
type ContentPart interface {
	contentPart()
}

// TextContent is plain text.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (TextContent) contentPart() {}

// ThinkingContent is model reasoning text that can be folded in the UI.
type ThinkingContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (ThinkingContent) contentPart() {}

// ImageContent is a base64-encoded image attachment.
type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mime_type"`
}

func (ImageContent) contentPart() {}

// ToolCallContent represents a requested tool invocation from the assistant.
type ToolCallContent struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (ToolCallContent) contentPart() {}

// ToolResultContent represents the outcome of a tool invocation.
type ToolResultContent struct {
	Type       string         `json:"type"`
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
	Content    []ContentPart  `json:"content"`
	IsError    bool           `json:"is_error"`
	Details    map[string]any `json:"details,omitempty"`
}

func (ToolResultContent) contentPart() {}

func (t ToolResultContent) Text() string {
	var out string
	for _, part := range t.Content {
		if text, ok := part.(TextContent); ok {
			out += text.Text
		}
	}
	return out
}

// ToolExecutionResult is the output returned by an executable tool.
type ToolExecutionResult struct {
	Content   []ContentPart  `json:"content"`
	Details   map[string]any `json:"details,omitempty"`
	Terminate bool           `json:"terminate"`
	// IsError marks the result as a failure the model should react to (e.g. a
	// non-zero shell exit). The registry propagates it to the tool_result's
	// is_error flag even when the tool returns a nil Go error.
	IsError bool `json:"is_error,omitempty"`
}

// Text returns the concatenated text of all text content parts.
func (r ToolExecutionResult) Text() string {
	var out string
	for _, part := range r.Content {
		if text, ok := part.(TextContent); ok {
			out += text.Text
		}
	}
	return out
}

// NewToolExecutionResultText creates a ToolExecutionResult containing a single text part.
func NewToolExecutionResultText(text string) ToolExecutionResult {
	return ToolExecutionResult{
		Content: []ContentPart{TextContent{Text: text}},
	}
}

// NewToolExecutionResultError creates a ToolExecutionResult representing an error.
func NewToolExecutionResultError(text string) ToolExecutionResult {
	return ToolExecutionResult{
		Content: []ContentPart{TextContent{Text: text}},
		IsError: true,
	}
}

// ExecutionMode controls how a tool participates in batch execution.
type ExecutionMode string

const (
	ExecutionParallel   ExecutionMode = "parallel"
	ExecutionSequential ExecutionMode = "sequential"
)

// ToolDefinition is the schema exposed to the LLM for a tool.
type ToolDefinition struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Parameters    map[string]any `json:"parameters"` // JSON Schema object
	ExecutionMode ExecutionMode  `json:"execution_mode"`
}

// ModelRef identifies a specific model for the LLM engine. Provider is
// optional: when empty, the engine resolves it from the model id.
type ModelRef struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id"`
}

func (m ModelRef) String() string {
	return fmt.Sprintf("%s/%s", m.Provider, m.ID)
}

// ModelInfo describes a model available via the LLM engine.
type ModelInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider"`
	Aliases       []string `json:"aliases,omitempty"`
	Capabilities  []string `json:"capabilities"`
	ContextWindow int      `json:"context_window"`
}

// GenerationConfig controls LLM output behavior.
type GenerationConfig struct {
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
}

// TurnRequest is sent to the LLM engine for a single provider turn.
type TurnRequest struct {
	Model            ModelRef         `json:"model"`
	SystemPrompt     string           `json:"system_prompt"`
	Messages         []AgentMessage   `json:"messages"`
	Tools            []ToolDefinition `json:"tools,omitempty"`
	Generation       GenerationConfig `json:"generation,omitempty"`
	Cache            string           `json:"cache,omitempty"`
	CacheBreakpoints []int            `json:"cache_breakpoints,omitempty"`
	// Thinking is the resolved thinking intent: "" (send nothing), "off",
	// "on", or a model-declared effort level. ThinkingOffEffort is the wire
	// encoding for "off" when the model declares one (e.g. "none"), filled by
	// the engine from the catalog.
	Thinking          string `json:"thinking,omitempty"`
	ThinkingOffEffort string `json:"thinking_off_effort,omitempty"`
}

// LLMUsage captures token and cost information from a provider turn.
type LLMUsage struct {
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	PromptCost       float64 `json:"prompt_cost"`
	CompletionCost   float64 `json:"completion_cost"`
	CacheReadCost    float64 `json:"cache_read_cost"`
	CacheWriteCost   float64 `json:"cache_write_cost"`
	TotalCost        float64 `json:"total_cost"`
}
