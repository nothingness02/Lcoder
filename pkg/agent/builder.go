package agent

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Builder constructs an Agent with a fluent, replaceable interface.
type Builder struct {
	cfg          Config
	llmClient    *llm.Client
	registry     *tools.Registry
	permissions  *permissions.Engine
	bus          *events.Bus
	obsCollector *observability.Collector
	contextMgr   *contextmgr.Manager
}

// NewBuilder creates a new Agent builder.
func NewBuilder() *Builder {
	return &Builder{
		cfg: Config{
			MaxTurns:          25,
			ToolExecutionMode: models.ExecutionParallel,
		},
	}
}

// WithConfig applies a full configuration.
func (b *Builder) WithConfig(cfg Config) *Builder {
	b.cfg = cfg
	return b
}

// WithSystemPrompt sets the system prompt.
func (b *Builder) WithSystemPrompt(prompt string) *Builder {
	b.cfg.SystemPrompt = prompt
	return b
}

// WithModel sets the model reference.
func (b *Builder) WithModel(provider, id string) *Builder {
	b.cfg.Model = models.ModelRef{Provider: provider, ID: id}
	return b
}

// WithMaxTurns sets the maximum number of turns.
func (b *Builder) WithMaxTurns(max int) *Builder {
	b.cfg.MaxTurns = max
	return b
}

// WithToolExecutionMode sets the default tool execution mode.
func (b *Builder) WithToolExecutionMode(mode models.ExecutionMode) *Builder {
	b.cfg.ToolExecutionMode = mode
	return b
}

// WithContextManager sets the context manager.
func (b *Builder) WithContextManager(mgr *contextmgr.Manager) *Builder {
	b.contextMgr = mgr
	b.cfg.ContextManager = mgr
	return b
}

// WithTransformContext sets the context transformer.
func (b *Builder) WithTransformContext(fn TransformContext) *Builder {
	b.cfg.TransformContext = fn
	return b
}

// WithBeforeToolCall sets the before-tool-call hook.
func (b *Builder) WithBeforeToolCall(fn BeforeToolCallHook) *Builder {
	b.cfg.BeforeToolCall = fn
	return b
}

// WithAfterToolCall sets the after-tool-call hook.
func (b *Builder) WithAfterToolCall(fn AfterToolCallHook) *Builder {
	b.cfg.AfterToolCall = fn
	return b
}

// WithShouldStop sets the stop predicate.
func (b *Builder) WithShouldStop(fn ShouldStopFunc) *Builder {
	b.cfg.ShouldStop = fn
	return b
}

// WithMode sets the active mode and mode manager.
func (b *Builder) WithMode(name string, mgr *ModeManager) *Builder {
	b.cfg.Mode = name
	b.cfg.ModeManager = mgr
	return b
}

// WithGatewayClient sets the LLM client.
func (b *Builder) WithGatewayClient(c *llm.Client) *Builder {
	b.llmClient = c
	return b
}

// WithRegistry sets the tool registry.
func (b *Builder) WithRegistry(r *tools.Registry) *Builder {
	b.registry = r
	return b
}

// WithDeferredTools enables deferred tool loading with the given core set.
// Passing no names uses DefaultCoreTools.
func (b *Builder) WithDeferredTools(core ...string) *Builder {
	b.cfg.DeferredTools = true
	b.cfg.CoreTools = core
	return b
}

// WithReminderProducer appends an ephemeral system-reminder producer.
func (b *Builder) WithReminderProducer(p ReminderProducer) *Builder {
	b.cfg.ReminderProducers = append(b.cfg.ReminderProducers, p)
	return b
}

// WithPermissions sets the permission engine.
func (b *Builder) WithPermissions(p *permissions.Engine) *Builder {
	b.permissions = p
	return b
}

// WithEventBus sets the event bus.
func (b *Builder) WithEventBus(bus *events.Bus) *Builder {
	b.bus = bus
	return b
}

// WithObservability sets the observability collector.
func (b *Builder) WithObservability(c *observability.Collector) *Builder {
	b.obsCollector = c
	return b
}

// WithSessionID identifies the session the agent belongs to. Used as the
// default key for automatic checkpoints.
func (b *Builder) WithSessionID(id string) *Builder {
	b.cfg.SessionID = id
	return b
}

// WithCheckpointStore enables automatic checkpoints between turns.
func (b *Builder) WithCheckpointStore(store checkpoint.Store) *Builder {
	b.cfg.CheckpointStore = store
	return b
}

// Build validates required fields and returns a configured Agent.
func (b *Builder) Build() (*Agent, error) {
	if b.llmClient == nil {
		return nil, fmt.Errorf("llm client is required")
	}
	if b.registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	if b.bus == nil {
		return nil, fmt.Errorf("event bus is required")
	}
	if b.permissions == nil {
		b.permissions = permissions.NewEngineFromRules(nil)
	}

	if b.cfg.Mode == "" {
		b.cfg.Mode = "code"
	}

	ag := New(b.cfg, b.llmClient, b.registry, b.permissions, b.bus)
	if b.obsCollector != nil {
		ag.obsCollector = b.obsCollector
		ag.emitter.obs = b.obsCollector
		ag.streamer.obs = b.obsCollector
	}
	return ag, nil
}
