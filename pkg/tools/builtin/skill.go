package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/tools"
)

// maxSkillBodyBytes caps the activated skill body so one giant skill cannot
// blow up the context window.
const maxSkillBodyBytes = 32 << 10

// UseSkill lets the model activate a skill from the catalog on its own. The
// catalog block in the system prompt is the discovery layer; this tool is the
// activation layer — it loads the skill body from disk and returns it as the
// tool result, so activation flows with the turn instead of the host writing
// a permanent system message into the session.
type UseSkill struct {
	catalog *skills.Catalog
}

// NewUseSkill builds the use_skill tool bound to the shared skill catalog.
// The catalog is read at call time, so runtime toggles take effect.
func NewUseSkill(cwd string, catalog *skills.Catalog) tools.Executable {
	return &UseSkill{catalog: catalog}
}

func (t *UseSkill) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: skills.UseSkillToolName,
		Description: "Load and activate a skill by name. The system prompt lists available skills; " +
			"when the user's request matches a skill's purpose, call this tool to load its full " +
			"instructions and follow them for the rest of the task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "Name of the skill to activate, exactly as listed in the catalog.",
				},
			},
			"required": []any{"skill_name"},
		},
		// Sequential so that within one batch an activation completes before
		// later calls are checked against the skill's tool restriction.
	}
}

func (t *UseSkill) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	name, _ := args["skill_name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return models.ToolExecutionResult{}, fmt.Errorf("missing skill_name")
	}
	meta, found := t.catalog.Find(name)
	if !found {
		return models.ToolExecutionResult{}, fmt.Errorf("unknown skill %q (available: %s)",
			name, t.availableNames())
	}
	if t.catalog.IsDisabled(meta.Name) {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"skill %q is disabled (enable it via /skills)", meta.Name)
	}
	if meta.DisableModelInvocation {
		return models.ToolExecutionResult{}, fmt.Errorf(
			"skill %q is manual-only (invoke it with /skill:%s)", meta.Name, meta.Name)
	}
	skill, err := skills.LoadSkill(meta.Source)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	text := skills.RenderActiveSkill(skill)
	if len(text) > maxSkillBodyBytes {
		text = text[:maxSkillBodyBytes] + "\n\n[truncated: skill body exceeds 32KB]"
	}
	if len(skill.AllowedTools) > 0 {
		text += fmt.Sprintf("\n\nThis skill restricts subsequent tool calls to: %s (plus %s). "+
			"Calls to other tools will be rejected until you activate a different skill.",
			strings.Join(skill.AllowedTools, ", "), skills.UseSkillToolName)
	}
	result := models.NewToolExecutionResultText(text)
	// Always publish the key on success: the executor replaces its active
	// filter with this list, so an empty list lifts a previous restriction.
	result.Details = map[string]any{skills.AllowedToolsDetailsKey: skill.AllowedTools}
	return result, nil
}

func (t *UseSkill) availableNames() string {
	entries := t.catalog.Entries()
	if len(entries) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

var _ tools.Executable = (*UseSkill)(nil)
