// Package agenthost implements the in-process subagent host.
// A spawned subagent gets a clean context, its own forked
// permission engine (shared rules, private guards), its own event bus, and
// its own journal id; the parent receives only a distilled summary plus
// usage.
package agenthost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lcoder/lcoder"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
)

// rolePrefixText loads the shared subagent role prefix embedded from
// configs/agents/_role_prefix.md (kimi-code's task-agent role prefix). Every
// subagent's system prompt leads with it so the model knows its user messages
// come from the parent agent and that only its last message travels back.
func rolePrefixText() string {
	data, err := lcoder.AgentProfiles.ReadFile("configs/agents/_role_prefix.md")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

const defaultSpawnTimeout = 30 * time.Minute

// HostConfig carries the process-wide dependencies a Host needs to spawn
// subagents.
type HostConfig struct {
	LLMClient   *llm.Client
	Registry    *tools.Registry
	ModeManager *agent.ModeManager
	Permissions *permissions.Engine // parent engine; Forked per spawn
	Model       models.ModelRef     // default model for children
	CWD         string
	// SessionStore persists subagent journals for resume; nil disables
	// resume (in-memory runs only).
	SessionStore *session.Store
	// Profiles resolves journal profile names back to profiles on resume.
	Profiles map[string]subagent.Agent
	// NewContextManager builds a fresh context manager for a child agent.
	// Wired from agentsetup in main so children share budget/compaction
	// policy without sharing any conversation state.
	NewContextManager func() *contextmgr.Manager
	// ParentBus receives mirrored subagent activity (see mirrorChild) so the
	// TUI can render it nested under the parent's subagent tool call. Nil
	// disables mirroring.
	ParentBus *events.Bus
}

// Host spawns and drives in-process subagents. It implements
// subagent.Spawner.
type Host struct {
	cfg        HostConfig
	confirm    agent.UserConfirmation
	beforeHook agent.BeforeToolCallHook
	afterHook  agent.AfterToolCallHook

	journal         *journalStore
	parentSessionID string
}

// NewHost creates a subagent host.
func NewHost(cfg HostConfig) *Host {
	return &Host{cfg: cfg, journal: newJournalStore()}
}

// SetParentSession records the parent agent's session id; resume validation
// rejects journals owned by a different parent.
func (h *Host) SetParentSession(id string) { h.parentSessionID = id }

// SetUserConfirm installs the interactive confirmation handler used by
// spawned children (wired alongside the parent's own SetUserConfirm).
func (h *Host) SetUserConfirm(uc agent.UserConfirmation) { h.confirm = uc }

// SetHooks installs the tool-call hooks used by spawned children (wired
// alongside the parent's own SetBeforeToolCall/SetAfterToolCall).
func (h *Host) SetHooks(before agent.BeforeToolCallHook, after agent.AfterToolCallHook) {
	h.beforeHook = before
	h.afterHook = after
}

var _ subagent.Spawner = (*Host)(nil)

// outcomeStatusOf maps an outcome to the group-display status string.
func outcomeStatusOf(out *subagent.Outcome) string {
	switch {
	case out.Err != nil:
		return "failed"
	case out.TimedOut:
		return "timeout"
	case out.Canceled:
		return "canceled"
	default:
		return "completed"
	}
}

// Spawn runs a single subagent to completion and returns its distilled
// summary. The child runs with a clean context, a wall-clock timeout, and an
// optional turn budget; a timed-out run still returns whatever summary the
// child produced so the parent can resume it later.
func (h *Host) Spawn(ctx context.Context, req subagent.SpawnRequest) *subagent.Outcome {
	agentID := "agent-" + uuid.NewString()[:8]
	var sess *session.Session
	cwd := req.CWD
	if cwd == "" {
		cwd = h.cfg.CWD
	}
	if h.cfg.SessionStore != nil {
		// Journals always live under the host's project: resume loads them
		// with LoadByID(h.cfg.CWD, ...), and a custom per-run cwd would make
		// the journal unreachable.
		if created, err := h.cfg.SessionStore.Create(h.cfg.CWD); err == nil {
			sess = created
			agentID = created.ID
		}
	}
	meta := journalMeta{ParentSessionID: h.parentSessionID, Profile: req.Profile.Name, Task: req.Task}
	return h.run(ctx, req.Profile, agentID, sess, meta, nil, req.Task, req.ParentToolCallID)
}

// Resume continues a subagent from its journal with a new instruction.
// Ownership (parent session), profile, and idleness are validated first
// (kimi-code's ensureOwnedIdleSubagent).
func (h *Host) Resume(ctx context.Context, req subagent.ResumeRequest) *subagent.Outcome {
	sess, meta, profile, err := h.validateResume(req.AgentID)
	if err != nil {
		return &subagent.Outcome{AgentID: req.AgentID, Err: err}
	}
	return h.run(ctx, profile, req.AgentID, sess, *meta, sess.EffectiveMessages(), req.Task, req.ParentToolCallID)
}

// run executes a subagent run (fresh or resumed) and persists the journal.
func (h *Host) run(ctx context.Context, profile subagent.Agent, agentID string, sess *session.Session, meta journalMeta, prior []models.AgentMessage, task, parentToolCallID string) *subagent.Outcome {
	if !h.journal.markRunning(agentID) {
		return &subagent.Outcome{AgentID: agentID, Err: fmt.Errorf("subagent %q is already running", agentID)}
	}
	defer h.journal.markIdle(agentID)

	timeout := time.Duration(profile.Timeout) * time.Second
	if timeout <= 0 {
		timeout = defaultSpawnTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	child := h.buildChild(profile, "")
	child.SetSessionID(agentID)
	if len(prior) > 0 {
		child.SetMessages(prior)
	}
	stopMirror := h.mirrorChild(child, agentID, profile.Name, parentToolCallID)

	var stopBudget func()
	if profile.MaxTurns > 0 {
		stopBudget = enforceTurnBudget(child, profile.MaxTurns)
	}

	prompt := models.NewAgentMessage(models.RoleUser, models.TextContent{Text: task})
	runErr := child.Prompt(ctx, prompt)

	out := &subagent.Outcome{
		AgentID:  agentID,
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	out.Canceled = !out.TimedOut && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled))
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		out.Err = runErr
	}
	if stopBudget != nil {
		stopBudget() // stop counting before summary retries, so they are not aborted
	}
	out.Summary = h.distillSummary(ctx, child, profile)
	out.Usage = child.Stats()
	stopMirror(outcomeStatusOf(out))

	if sess != nil {
		meta.Task = task
		if err := sess.AppendMissing(child.AllMessages()); err == nil {
			_ = writeMeta(sess, meta)
		}
	}
	return out
}

// buildChild constructs the child agent: forked permission engine (shared
// rules, private guards), isolated event bus, clean context, and the
// subagent tool stripped when the profile may not nest further.
func (h *Host) buildChild(profile subagent.Agent, cwd string) *agent.Agent {
	registry := h.cfg.Registry
	if len(profile.Subagents) == 0 {
		registry = registry.Without("subagent")
	}
	childCfg := agent.Config{
		BaseSystemPrompt:  rolePrefixText() + "\n\n" + profile.Prompt,
		Model:             h.resolveModel(profile),
		ToolExecutionMode: models.ExecutionParallel,
		ContextManager:    h.cfg.NewContextManager(),
		Mode:              profile.Mode,
		ModeManager:       h.cfg.ModeManager,
		UserConfirm:       h.confirm,
		BeforeToolCall:    h.beforeHook,
	}
	perms := h.cfg.Permissions.Fork()
	return agent.New(childCfg, h.cfg.LLMClient, registry, perms, events.New())
}

// resolveModel honors the profile's model preference, defaulting to the
// parent's model. A profile "model" value is a concrete model id on the
// parent's provider.
func (h *Host) resolveModel(profile subagent.Agent) models.ModelRef {
	if profile.Model == "" || profile.Model == "inherit" {
		return h.cfg.Model
	}
	return models.ModelRef{Provider: h.cfg.Model.Provider, ID: profile.Model}
}

// distillSummary extracts the child's final answer and enforces the
// profile's summary floor: too-short answers get follow-up turns asking for
// a proper write-up (kimi-code's summaryPolicy).
func (h *Host) distillSummary(ctx context.Context, child *agent.Agent, profile subagent.Agent) string {
	summary := lastAssistantText(child)
	for i := 0; i < profile.SummaryRetries && len(summary) < profile.SummaryMinChars; i++ {
		_ = child.Prompt(ctx, models.NewAgentMessage(models.RoleUser, models.TextContent{
			Text: fmt.Sprintf("Your last message is too short for the parent agent to use. Summarize the outcome of the task in at least %d characters: what was done, key findings, and anything the parent should act on.", profile.SummaryMinChars),
		}))
		summary = lastAssistantText(child)
	}
	return summary
}

// lastAssistantText returns the text of the child's last assistant message.
func lastAssistantText(child *agent.Agent) string {
	msg, ok := child.LatestAssistantMessage()
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, part := range msg.Content {
		if t, ok := part.(models.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// enforceTurnBudget watches the child's turn events and, when the turn
// budget is exhausted, steers it to wrap up instead of hard-killing; a child
// that keeps calling tools anyway is aborted one turn later. The returned
// function detaches the watcher (call it before summary retries so they are
// not counted — or aborted).
func enforceTurnBudget(child *agent.Agent, maxTurns int) (stop func()) {
	turns := 0
	return child.Subscribe(func(ctx context.Context, ev events.Event) error {
		if ev.EventType() != events.TurnEnd {
			return nil
		}
		turns++
		switch {
		case turns == maxTurns:
			child.Steer(models.NewAgentMessage(models.RoleUser, models.TextContent{
				Text: "Turn budget exhausted. Stop calling tools and use your last message to summarize the results you have so far.",
			}))
		case turns > maxTurns+1:
			child.Abort()
		}
		return nil
	})
}
