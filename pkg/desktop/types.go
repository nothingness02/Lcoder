package desktop

// UIMessage is the frontend-friendly representation of one conversation block.
type UIMessage struct {
	ID         string        `json:"id"`
	Role       string        `json:"role"`
	Text       string        `json:"text,omitempty"`
	Thinking   string        `json:"thinking,omitempty"`
	ToolCalls  []UIToolCall  `json:"tool_calls,omitempty"`
	ToolResult *UIToolResult `json:"tool_result,omitempty"`
	Timestamp  int64         `json:"timestamp,omitempty"`
}

// UIToolCall describes a tool invocation from the assistant.
type UIToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Result    *UIToolResult  `json:"result,omitempty"`
}

// UIToolResult describes the output of a tool call.
type UIToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
}

// SessionSummary is displayed in the session sidebar.
type SessionSummary struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
}

// UIConfig is the read-only runtime config shown in the status bar.
type UIConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	CWD      string `json:"cwd"`
}

// PermissionRequest is sent to the frontend when a tool needs approval.
type PermissionRequest struct {
	ID       string         `json:"id"`
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
}
