package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
)

// SwarmToolName is the batch delegation tool: one prompt template plus a list
// of items, expanded into one subagent run per item — with existing agent
// resumes optionally mixed in (kimi-code's AgentSwarm).
const SwarmToolName = "subagent_swarm"

// itemPlaceholder is the substitution point inside prompt_template.
const itemPlaceholder = "{{item}}"

// defaultSwarmProfile is used when subagent_type is omitted.
const defaultSwarmProfile = "coder"

// maxSwarmItems bounds a single swarm call (MAX_AGENT_SWARM_SUBAGENTS).
const maxSwarmItems = 128

// Swarm launches a batch of subagents from one prompt template over a list of
// items, optionally mixed with resumes of existing agents (resume_agent_ids).
// Each item runs as a full in-process subagent with a clean context; only the
// distilled results come back, aggregated into one XML block. A swarm call
// must be the only tool call in its response (enforced by the executor's
// swarm-exclusivity veto).
//
// Execution goes through the coordinated batch scheduler (subagent.Batch):
// burst-then-throttle launch, and on provider rate limits an exponential-
// backoff requeue that resumes each agent's journal under a shrinking
// capacity.
type Swarm struct {
	cwd      string
	spawner  subagent.Spawner
	profiles map[string]subagent.Agent

	// suspend, when set, is notified when a rate-limited item is requeued
	// (wired to the event bus in main).
	suspend func(agentID, reason string)
}

// NewSwarm creates the subagent_swarm tool bound to a spawner and the
// discovered profiles.
func NewSwarm(cwd string, spawner subagent.Spawner, profiles map[string]subagent.Agent) *Swarm {
	return &Swarm{cwd: cwd, spawner: spawner, profiles: profiles}
}

// SetSuspendNotifier installs the rate-limit requeue callback.
func (s *Swarm) SetSuspendNotifier(fn func(agentID, reason string)) { s.suspend = fn }

// Definition returns the tool schema exposed to the LLM.
func (s *Swarm) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        SwarmToolName,
		Description: s.description(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subagent_type": map[string]any{
					"type":        "string",
					"description": "Subagent profile to run for every spawned item (see the list above; defaults to \"" + defaultSwarmProfile + "\")",
				},
				"prompt_template": map[string]any{
					"type":        "string",
					"description": "One shared briefing containing {{item}} as the placeholder. Each item expands into a separate subagent run. IMPORTANT: a swarm call must be the ONLY tool call in your response.",
				},
				"items": map[string]any{
					"type":        "array",
					"description": "The values substituted into {{item}} (e.g. file paths, symbol names). At least 2, at most 128; every expanded prompt must be distinct.",
					"items":       map[string]any{"type": "string"},
				},
				"resume_agent_ids": map[string]any{
					"type":                 "object",
					"description":          "Map of agent_id -> prompt for subagents to resume from earlier work (e.g. ones that failed or timed out). May be combined with items in the same call; do not duplicate resumed work in items.",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Short human-readable description of the whole swarm (shown in the UI)",
				},
			},
			"required": []string{},
		},
	}
}

// description builds the tool description with the discovered profiles
// inlined, mirroring kimi's agent-swarm.md guidance.
func (s *Swarm) description() string {
	var b strings.Builder
	b.WriteString("Launch multiple subagents from one prompt template, existing agent resumes, or both. " +
		"Use this when many subagents should run the same kind of task over different inputs; the placeholder is exactly " +
		itemPlaceholder + ". For a few differently-shaped tasks, make separate subagent calls in one message instead.\n" +
		"Each of these is enforced: provide at least 2 items unless you pass resume_agent_ids; whenever items are present, " +
		"prompt_template is required and must contain " + itemPlaceholder + "; and the filled-in prompts must be distinct. " +
		"Supports up to 128 subagents, and launches are queued automatically. " +
		"IMPORTANT: a swarm call must be the ONLY tool call in the response.\n\n" +
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

// Execute validates the swarm arguments, expands them into per-agent specs,
// and runs the batch through the coordinated scheduler.
func (s *Swarm) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	specs, profile, err := s.buildSpecs(args)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}
	tasks := make([]subagent.BatchTask, len(specs))
	for i, spec := range specs {
		tasks[i] = subagent.BatchTask{
			Profile:   profile,
			Prompt:    spec.prompt,
			SwarmItem: spec.item,
			ResumeID:  spec.resumeID,
		}
	}
	b := &subagent.Batch{
		Launcher: swarmLauncher{spawner: s.spawner, cwd: s.cwd, parentCallID: callID},
		Suspend:  s.suspend,
	}
	results := b.Run(ctx, tasks)
	return models.NewToolExecutionResultText(formatSwarmResults(profile.Name, specs, results)), nil
}

// swarmLauncher adapts the subagent.Spawner to the batch Launcher.
type swarmLauncher struct {
	spawner      subagent.Spawner
	cwd          string
	parentCallID string
}

func (l swarmLauncher) Spawn(ctx context.Context, task subagent.BatchTask) *subagent.Outcome {
	return l.spawner.Spawn(ctx, subagent.SpawnRequest{
		Profile: task.Profile, Task: task.Prompt, CWD: l.cwd,
		ParentToolCallID: l.parentCallID, SwarmItem: task.SwarmItem,
	})
}

func (l swarmLauncher) Resume(ctx context.Context, agentID, prompt string) *subagent.Outcome {
	return l.spawner.Resume(ctx, subagent.ResumeRequest{AgentID: agentID, Task: prompt, ParentToolCallID: l.parentCallID})
}

// swarmSpec is one expanded swarm unit: a fresh spawn (from items) or a
// resume of an existing agent (from resume_agent_ids).
type swarmSpec struct {
	kind     string // "spawn" | "resume"
	item     string
	prompt   string
	resumeID string
}

// resumeEntry is a parsed resume_agent_ids entry, sorted by id for
// deterministic output (JSON object iteration order is unspecified).
type resumeEntry struct {
	id     string
	prompt string
}

// buildSpecs validates the swarm arguments and expands them into per-agent
// specs: resumes first (resume_agent_ids, sorted by id), then spawns (items).
// Validation mirrors kimi's createAgentSwarmSpecs:
//
//   - at least 2 items unless resume_agent_ids is provided;
//   - whenever items are present, prompt_template is required and must
//     contain {{item}};
//   - the filled-in prompts must be distinct;
//   - the total (resumes + items) is at most 128.
func (s *Swarm) buildSpecs(args map[string]any) ([]swarmSpec, subagent.Agent, error) {
	agentName := tools.String(args, "subagent_type", defaultSwarmProfile)
	profile, ok := s.profiles[agentName]
	if !ok {
		return nil, subagent.Agent{}, fmt.Errorf(
			"unknown subagent type %q; available: %s", agentName, strings.Join(sortedProfileNames(s.profiles), ", "))
	}

	resumeEntries := parseResumeEntries(args["resume_agent_ids"])
	items, err := parseSwarmItems(args["items"])
	if err != nil {
		return nil, subagent.Agent{}, err
	}

	itemCount := len(items)
	resumeCount := len(resumeEntries)
	if itemCount == 0 && resumeCount == 0 {
		return nil, subagent.Agent{}, fmt.Errorf(
			"subagent_swarm: provide at least 2 items or a non-empty resume_agent_ids")
	}
	if itemCount == 1 && resumeCount == 0 {
		return nil, subagent.Agent{}, fmt.Errorf(
			"subagent_swarm: swarm needs at least 2 items; use the subagent tool for a single task")
	}
	if itemCount+resumeCount > maxSwarmItems {
		return nil, subagent.Agent{}, fmt.Errorf(
			"subagent_swarm: too many subagents (%d > %d)", itemCount+resumeCount, maxSwarmItems)
	}

	var template string
	if itemCount > 0 {
		template, err = tools.RequiredString(args, "prompt_template")
		if err != nil {
			return nil, subagent.Agent{}, fmt.Errorf("subagent_swarm: %w", err)
		}
		if !strings.Contains(template, itemPlaceholder) {
			return nil, subagent.Agent{}, fmt.Errorf(
				"subagent_swarm: prompt_template must contain %s as the item placeholder", itemPlaceholder)
		}
	}

	specs := make([]swarmSpec, 0, itemCount+resumeCount)
	for _, re := range resumeEntries {
		specs = append(specs, swarmSpec{kind: "resume", prompt: re.prompt, resumeID: re.id})
	}
	seen := make(map[string]string, itemCount)
	for _, item := range items {
		prompt := strings.ReplaceAll(template, itemPlaceholder, item)
		if prev, dup := seen[prompt]; dup {
			return nil, subagent.Agent{}, fmt.Errorf(
				"subagent_swarm: items %q and %q expand to the same prompt; make each item distinct", prev, item)
		}
		seen[prompt] = item
		specs = append(specs, swarmSpec{kind: "spawn", item: item, prompt: prompt})
	}
	return specs, profile, nil
}

// parseResumeEntries reads and normalizes resume_agent_ids: a map of
// agent_id -> prompt. Entries are sorted by id for deterministic ordering.
func parseResumeEntries(raw any) []resumeEntry {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out []resumeEntry
	for id, v := range m {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		prompt := strings.TrimSpace(fmt.Sprint(v))
		out = append(out, resumeEntry{id: id, prompt: prompt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// parseSwarmItems validates the items array: absent (nil) means no items
// (a resume-only swarm is allowed); when present, it must be a non-empty
// array of non-empty strings.
func parseSwarmItems(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	rawItems, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("subagent_swarm: items must be an array of strings")
	}
	if len(rawItems) == 0 {
		return nil, fmt.Errorf("subagent_swarm: items must not be empty when provided")
	}
	items := make([]string, 0, len(rawItems))
	for i, entry := range rawItems {
		item, ok := entry.(string)
		if !ok || strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("subagent_swarm: items[%d] must be a non-empty string", i)
		}
		items = append(items, item)
	}
	return items, nil
}

// formatSwarmResults renders the aggregated result for the parent model:
// a summary line plus one <subagent> block per unit, tagged with
// mode="resume" when the unit was a resume and item= when it was a spawn.
// Failures carry a resume_agent_ids hint so the model can continue them.
func formatSwarmResults(profile string, specs []swarmSpec, results []subagent.BatchResult) string {
	completed, failed := 0, 0
	for _, r := range results {
		if r.Status == subagent.StatusCompleted {
			completed++
		} else {
			failed++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<subagent_results profile=%q completed=\"%d\" failed=\"%d\">\n", profile, completed, failed)
	for i, r := range results {
		spec := specs[i]
		mode := ""
		if spec.kind == "resume" {
			mode = " mode=\"resume\""
		}
		itemAttr := ""
		if spec.item != "" {
			itemAttr = fmt.Sprintf(" item=%q", spec.item)
		}
		fmt.Fprintf(&b, "<subagent%s agent_id=%q%s outcome=%q>\n", mode, r.AgentID, itemAttr, r.Status)
		if r.Status == subagent.StatusCompleted {
			fmt.Fprintf(&b, "<summary>\n%s\n</summary>\n", r.Summary)
		} else {
			errText := r.Error
			if errText == "" {
				errText = string(r.Status)
			}
			fmt.Fprintf(&b, "<error>%s; resume with subagent_swarm resume_agent_ids (agent_id=%s)</error>\n", errText, r.AgentID)
		}
		b.WriteString("</subagent>\n")
	}
	b.WriteString("</subagent_results>")
	return b.String()
}

var _ tools.Executable = (*Swarm)(nil)
