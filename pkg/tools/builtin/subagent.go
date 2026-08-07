package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Subagent delegates self-contained tasks to in-process subagents: full
// agent instances with clean contexts, their own permission surface (from
// their profile's mode), and their own budgets. Only the subagents' final
// messages come back. For many similar sub-tasks use the separate
// subagent_swarm tool instead.
type Subagent struct {
	cwd      string
	spawner  subagent.Spawner
	profiles map[string]subagent.Agent

	bgMu      sync.Mutex
	bgResults []string // completed background notifications, drained by the parent loop

	// notify, when set, is called immediately on background completion so
	// the UI can surface the result without waiting for a turn boundary.
	notify func(text string)
}

// NewSubagent creates the subagent tool bound to a spawner and the
// discovered profiles.
func NewSubagent(cwd string, spawner subagent.Spawner, profiles map[string]subagent.Agent) *Subagent {
	return &Subagent{cwd: cwd, spawner: spawner, profiles: profiles}
}

// SetNotifier installs the immediate-completion callback for background
// subagents (wired to the event bus in main).
func (s *Subagent) SetNotifier(fn func(text string)) { s.notify = fn }

// DrainNotifications returns and clears completed background subagent
// results. The parent agent calls this once per turn (reminder producer) so
// background completions arrive automatically instead of being polled.
func (s *Subagent) DrainNotifications() []string {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	out := s.bgResults
	s.bgResults = nil
	return out
}

func (s *Subagent) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "subagent",
		Description: s.description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{
					"type":        "string",
					"description": "Subagent profile to run (see the list above)",
				},
				"task": map[string]any{
					"type":        "string",
					"description": "Complete, self-contained task briefing for the subagent (single form)",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Working directory for the subagent's file tools and permission boundary (defaults to the parent's working directory). Paths are resolved relative to it; absolute paths outside it are allowed but require explicit permission.",
				},
				"resume": map[string]any{
					"type":        "string",
					"description": "agent_id of a previous subagent run to continue with full context; when set, agent is ignored",
				},
				"run_in_background": map[string]any{
					"type": "boolean",
					"description": "Start the subagent in the background and return immediately. The result arrives " +
						"automatically as a reminder — do NOT poll for it.",
				},
			},
			"required": []string{},
		},
	}
}

// description builds the tool description with the discovered profiles
// inlined, so the parent model can make an informed choice (kimi-code's
// dynamic profile listing). Batch delegation points to the separate
// subagent_swarm tool.
func (s *Subagent) description() string {
	var b strings.Builder
	b.WriteString("Delegate one self-contained task to a subagent. A subagent starts with zero context — " +
		"it has NOT seen this conversation, so the task must brief it like a colleague who just walked in: " +
		"goal, relevant file paths, constraints, and exactly what to return. The subagent works autonomously " +
		"(it cannot ask you questions) and only its final message comes back. " +
		"For many similar sub-tasks run over different inputs, use the " + SwarmToolName + " tool instead." +
		" For a few differently-shaped tasks, make several subagent calls in one message.\n\n" +
		"Available agent types:\n")
	for _, name := range sortedProfileNames(s.profiles) {
		p := s.profiles[name]
		desc := p.Description
		if desc == "" {
			desc = "no description"
		}
		fmt.Fprintf(&b, "- %s (mode: %s): %s\n", name, p.Mode, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Subagent) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	// The swarm form moved to the separate subagent_swarm tool; redirect the
	// model instead of silently ignoring the arguments.
	if _, hasTemplate := args["prompt_template"]; hasTemplate {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"subagent: swarm form moved to the " + SwarmToolName + " tool (prompt_template + items + optional resume_agent_ids)")
	}
	if _, hasItems := args["items"]; hasItems {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"subagent: items requires the " + SwarmToolName + " tool with a prompt_template containing {{item}}")
	}

	task, err := tools.RequiredString(args, "task")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	if resume := tools.String(args, "resume", ""); resume != "" {
		out := s.spawner.Resume(ctx, subagent.ResumeRequest{AgentID: resume, Task: task, ParentToolCallID: callID})
		return models.NewToolExecutionResultText(formatSpawnOutcome(out)), nil
	}

	agentName, err := tools.RequiredString(args, "agent")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	profile, ok := s.profiles[agentName]
	if !ok {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"unknown subagent %q; available: %s", agentName, strings.Join(sortedProfileNames(s.profiles), ", "))
	}

	cwd := s.cwd
	if v := tools.String(args, "cwd", ""); v != "" {
		cwd = resolveInCwd(s.cwd, v)
	}

	req := subagent.SpawnRequest{Profile: profile, Task: task, CWD: cwd, ParentToolCallID: callID}
	if tools.Bool(args, "run_in_background", false) {
		return models.NewToolExecutionResultText(s.startBackground(req)), nil
	}

	out := s.spawner.Spawn(ctx, req)
	return models.NewToolExecutionResultText(formatSpawnOutcome(out)), nil
}

// --- background ---

// startBackground launches the spawn in a goroutine and returns immediately;
// completion is queued for DrainNotifications.
func (s *Subagent) startBackground(req subagent.SpawnRequest) string {
	id := "bg-" + uuid.NewString()[:8]
	go func() {
		out := s.spawner.Spawn(context.Background(), req)
		var note string
		switch {
		case out.Err != nil:
			note = fmt.Sprintf("background subagent %s (%s) FAILED: %v (agent_id=%s; resume with it to continue)", id, req.Profile.Name, out.Err, out.AgentID)
		case out.TimedOut:
			note = fmt.Sprintf("background subagent %s (%s) timed out (agent_id=%s; resume with it to continue)", id, req.Profile.Name, out.AgentID)
		default:
			note = fmt.Sprintf("background subagent %s (%s) completed:\n%s", id, req.Profile.Name, out.Summary)
		}
		s.bgMu.Lock()
		s.bgResults = append(s.bgResults, note)
		s.bgMu.Unlock()
		if s.notify != nil {
			s.notify(note)
		}
	}()
	return fmt.Sprintf("started background subagent %s (profile: %s). Its result will arrive automatically as a reminder — do not poll for it.", id, req.Profile.Name)
}

// --- shared formatting ---

// sortedProfileNames returns the profile names sorted alphabetically.
func sortedProfileNames(profiles map[string]subagent.Agent) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func outcomeStatus(out *subagent.Outcome) string {
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

// formatSpawnOutcome renders the outcome for the parent model, always
// carrying the agent_id so a timed-out or failed run can be resumed later.
func formatSpawnOutcome(out *subagent.Outcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent_id: %s\n", out.AgentID)
	switch {
	case out.Err != nil:
		fmt.Fprintf(&b, "status: failed (%v)\n", out.Err)
	case out.TimedOut:
		b.WriteString("status: timeout\n")
	default:
		b.WriteString("status: completed\n")
	}
	if out.Summary != "" {
		fmt.Fprintf(&b, "summary:\n%s", out.Summary)
	} else if out.Err != nil || out.TimedOut {
		b.WriteString("summary: (none produced)\n")
	}
	if out.TimedOut || out.Err != nil {
		fmt.Fprintf(&b, "\nresume: continue this subagent with {\"task\": \"...\", \"resume\": \"%s\"}", out.AgentID)
	}
	return b.String()
}

// DeclareAccesses implements tools.AccessDeclarer: a subagent spawn runs in
// its own process/cwd context, so the parent-side batch scheduler treats it
// as touching no resource. Independent subagent calls therefore run in
// parallel (alignment with kimi's Promise.allSettled); avoiding conflicting
// file writes across subagents is the parent model's responsibility, per the
// swarm guidance.
func (s *Subagent) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	return []tools.ToolAccess{{Op: tools.OpNone}}
}

var _ tools.Executable = (*Subagent)(nil)
var _ tools.AccessDeclarer = (*Subagent)(nil)
