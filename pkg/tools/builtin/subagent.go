package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
)

const (
	// swarmConcurrency caps how many subagents one swarm call runs at once
	// (provider-rate-limit adaptation is a later iteration once the llm
	// layer exposes structured errors).
	swarmConcurrency = 4
	// retryBackoff is the delay before a failed swarm item is retried by
	// resuming its own journal (partial progress is preserved).
	retryBackoff = 3 * time.Second
	// maxSwarmItems bounds a single swarm call (kimi-code's MAX_AGENT_SWARM_SUBAGENTS).
	maxSwarmItems = 128
)

// Subagent delegates self-contained tasks to in-process subagents: full
// agent instances with clean contexts, their own permission surface (from
// their profile's mode), and their own budgets. Only the subagents' final
// messages come back.
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
					"description": "Working directory for the subagent (defaults to the project root)",
				},
				"resume": map[string]any{
					"type":        "string",
					"description": "agent_id of a previous subagent run to continue with full context; when set, agent is ignored",
				},
				"prompt_template": map[string]any{
					"type": "string",
					"description": "Swarm form: one shared briefing containing {{item}} as the placeholder. Each item expands " +
						"into a separate subagent run. IMPORTANT: a swarm call must be the ONLY tool call in your response.",
				},
				"items": map[string]any{
					"type": "array",
					"description": "Swarm form: the values substituted into {{item}} (e.g. file paths, symbol names). " +
						"At least 2, at most 128; every expanded prompt must be distinct.",
					"items": map[string]any{"type": "string"},
				},
				"run_in_background": map[string]any{
					"type": "boolean",
					"description": "Start the subagent in the background and return immediately. The result arrives " +
						"automatically as a reminder — do NOT poll for it.",
				},
			},
			"required": []string{},
		},
		ExecutionMode: models.ExecutionSequential,
	}
}

// description builds the tool description with the discovered profiles
// inlined, so the parent model can make an informed choice (kimi-code's
// dynamic profile listing).
func (s *Subagent) description() string {
	var b strings.Builder
	b.WriteString("Delegate self-contained tasks to subagents. A subagent starts with zero context — " +
		"it has NOT seen this conversation, so the task must brief it like a colleague who just walked in: " +
		"goal, relevant file paths, constraints, and exactly what to return. The subagent works autonomously " +
		"(it cannot ask you questions) and only its final message comes back. " +
		"For several similar sub-tasks, use the swarm form: one prompt_template with {{item}} plus an items list.\n\n" +
		"Available agent types:\n")
	for _, name := range s.profileNames() {
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
	// Swarm form: prompt_template + items.
	if _, hasTemplate := args["prompt_template"]; hasTemplate {
		return s.executeSwarm(ctx, callID, args)
	}
	if _, hasItems := args["items"]; hasItems {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: items requires prompt_template containing {{item}}")
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
			"unknown subagent %q; available: %s", agentName, strings.Join(s.profileNames(), ", "))
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

// --- swarm form (kimi-code's AgentSwarm: prompt_template + {{item}} + items) ---

const itemPlaceholder = "{{item}}"

// executeSwarm validates the swarm arguments and runs the expanded prompts
// with a bounded-concurrency scheduler.
func (s *Subagent) executeSwarm(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	agentName, err := tools.RequiredString(args, "agent")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	profile, ok := s.profiles[agentName]
	if !ok {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"unknown subagent %q; available: %s", agentName, strings.Join(s.profileNames(), ", "))
	}
	template, err := tools.RequiredString(args, "prompt_template")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	if !strings.Contains(template, itemPlaceholder) {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"subagent: prompt_template must contain %s as the item placeholder", itemPlaceholder)
	}

	raw, ok := args["items"].([]any)
	if !ok || len(raw) == 0 {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: items is required and must be a non-empty array of strings")
	}
	items := make([]string, 0, len(raw))
	for i, entry := range raw {
		item, ok := entry.(string)
		if !ok || strings.TrimSpace(item) == "" {
			return models.ToolExecutionResult{}, fmt.Errorf("subagent: items[%d] must be a non-empty string", i)
		}
		items = append(items, item)
	}
	if len(items) < 2 {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: swarm needs at least 2 items; use the single form for one task")
	}
	if len(items) > maxSwarmItems {
		return models.ToolExecutionResult{}, fmt.Errorf("subagent: too many items (%d > %d)", len(items), maxSwarmItems)
	}

	// Expand and reject duplicated prompts (kimi's distinctness check).
	prompts := make([]string, len(items))
	seen := make(map[string]string, len(items))
	for i, item := range items {
		prompts[i] = strings.ReplaceAll(template, itemPlaceholder, item)
		if prev, dup := seen[prompts[i]]; dup {
			return models.ToolExecutionResult{}, fmt.Errorf(
				"subagent: items %q and %q expand to the same prompt; make each item distinct", prev, item)
		}
		seen[prompts[i]] = item
	}

	return models.NewToolExecutionResultText(s.runSwarm(ctx, callID, profile, items, prompts)), nil
}

// runSwarm executes the expanded prompts with bounded concurrency. Failures
// are isolated per item; a failed item is retried once by resuming its own
// journal so partial progress is not thrown away.
func (s *Subagent) runSwarm(ctx context.Context, parentCallID string, profile subagent.Agent, items, prompts []string) string {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(swarmConcurrency)
	outs := make([]*subagent.Outcome, len(items))
	for i := range items {
		i := i
		g.Go(func() error {
			outs[i] = s.runSwarmItem(ctx, parentCallID, profile, prompts[i])
			return nil
		})
	}
	_ = g.Wait()
	return formatSwarmResults(profile.Name, items, outs)
}

func (s *Subagent) runSwarmItem(ctx context.Context, parentCallID string, profile subagent.Agent, prompt string) *subagent.Outcome {
	out := s.spawner.Spawn(ctx, subagent.SpawnRequest{
		Profile:          profile,
		Task:             prompt,
		CWD:              s.cwd,
		ParentToolCallID: parentCallID,
	})
	if out.Err == nil && !out.TimedOut {
		return out
	}
	select {
	case <-ctx.Done():
		return out
	case <-time.After(retryBackoff):
	}
	retry := s.spawner.Resume(ctx, subagent.ResumeRequest{
		AgentID:          out.AgentID,
		Task:             "Continue and finish the original task: " + prompt,
		ParentToolCallID: parentCallID,
	})
	if retry.Err == nil && !retry.TimedOut {
		return retry
	}
	return out
}

func formatSwarmResults(profile string, items []string, outs []*subagent.Outcome) string {
	completed, failed := 0, 0
	for _, out := range outs {
		if out.Err == nil && !out.TimedOut && !out.Canceled {
			completed++
		} else {
			failed++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<subagent_results profile=%q completed=\"%d\" failed=\"%d\">\n", profile, completed, failed)
	for i, out := range outs {
		fmt.Fprintf(&b, "<subagent agent_id=\"%s\" item=\"%s\" outcome=\"%s\">\n",
			out.AgentID, items[i], outcomeStatus(out))
		if out.Err == nil && !out.TimedOut {
			fmt.Fprintf(&b, "<summary>\n%s\n</summary>\n", out.Summary)
		} else {
			errText := "timeout"
			if out.Err != nil {
				errText = out.Err.Error()
			}
			fmt.Fprintf(&b, "<error>%s; resume with {\"task\": \"...\", \"resume\": \"%s\"}</error>\n", errText, out.AgentID)
		}
		b.WriteString("</subagent>\n")
	}
	b.WriteString("</subagent_results>")
	return b.String()
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

func (s *Subagent) profileNames() []string {
	names := make([]string, 0, len(s.profiles))
	for name := range s.profiles {
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

var _ tools.Executable = (*Subagent)(nil)
