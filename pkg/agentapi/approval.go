// Package agentapi is the protocol boundary between the agent core and any UI
// (TUI, headless driver, future stdio/JSONL transport). It defines the CoreAPI
// interface, its DTOs, and the approval request/response types.
//
// Import discipline: this package may only import leaf shared packages
// (pkg/models, pkg/events, pkg/task, pkg/contextmgr, pkg/checkpoint). It must
// NEVER import pkg/agent — the dependency direction is
//
//	pkg/tui → pkg/agentapi ← pkg/agent
//
// so the UI depends on the protocol, not on the agent implementation.
package agentapi

import (
	"context"

	"github.com/lcoder/lcoder/pkg/models"
)

// UserConfirmation handles interactive permission approvals for tool calls.
type UserConfirmation interface {
	Confirm(ctx context.Context, info ToolCallInfo) (allow bool, err error)
	ConfirmWithScope(ctx context.Context, info ToolCallInfo) (ConfirmResult, error)
}

// ToolCallInfo is provided to hooks.
type ToolCallInfo struct {
	AssistantMessage models.AgentMessage
	ToolCall         models.ToolCallContent
	Args             map[string]any
	Context          []models.AgentMessage
}

// BashCommand returns the bash command from the tool call, or an empty string
// if the tool is not bash or has no command argument.
func (info ToolCallInfo) BashCommand() string {
	if info.ToolCall.Name != "bash" {
		return ""
	}
	if cmd, _ := info.Args["command"].(string); cmd != "" {
		return cmd
	}
	if cmd, _ := info.ToolCall.Arguments["command"].(string); cmd != "" {
		return cmd
	}
	return ""
}

// ConfirmScope describes how widely a user-approved permission should apply.
type ConfirmScope int

const (
	ScopeDeny ConfirmScope = iota
	ScopeOnce
	// ScopeSession approves the exact call for the rest of the session
	// (in-memory, never persisted).
	ScopeSession
	// ScopeProject writes a generalized allow rule into the project's
	// .lcoder/permissions.yaml (permanent, this machine only).
	ScopeProject
	// ScopeGlobal writes a generalized allow rule into the user-level
	// permissions file (permanent, all projects).
	ScopeGlobal
)

// ConfirmResult is the outcome of a scoped confirmation.
type ConfirmResult struct {
	Allow bool
	Scope ConfirmScope
}
