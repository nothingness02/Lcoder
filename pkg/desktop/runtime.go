package desktop

import (
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/session"
)

// AgentSetup holds the wired agent runtime. It is intentionally agnostic to
// whether the consumer is a CLI, TUI, or desktop app.
type AgentSetup struct {
	Agent           *agent.Agent
	Session         *session.Session
	SessionStore    *session.Store
	Bus             *events.Bus
	Config          config.Config
	CWD             string
	LLMClient       *llm.Client
	CheckpointStore checkpointStore
	ModeManager     *agent.ModeManager
	MCPRegistry     *mcp.Registry
	Cleanup         func()
}

type checkpointStore interface {
	Load(id string) (*checkpoint.Checkpoint, error)
	Save(id string, cp *checkpoint.Checkpoint) error
}

// Runtime extends AgentSetup with desktop-specific collaborators.
type Runtime struct {
	*AgentSetup
	Permissions *PermissionResponder
}

func NewRuntime(setup *AgentSetup) *Runtime {
	return &Runtime{
		AgentSetup:  setup,
		Permissions: NewPermissionResponder(setup.Bus),
	}
}
