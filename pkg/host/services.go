package host

import (
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/skills"
)

// Services bundles the local workbench services a UI needs alongside the
// agentapi.CoreAPI handle: the event bus the core emits on, and the
// provider/MCP/skills/config panels' backends. These are process-local
// services, not agent state, so they travel beside the protocol handle rather
// than through it (see plan P6). Modes is a startup snapshot of the mode
// catalog (modes do not change at runtime).
type Services struct {
	Bus          *events.Bus
	LLMClient    *llm.Client
	MCPRegistry  *mcp.Registry
	SkillCatalog *skills.Catalog
	Config       config.Config
	Modes        []agentapi.ModeInfo
}
