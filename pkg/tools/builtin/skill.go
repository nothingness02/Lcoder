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

// UseSkill lets the model activate a skill from the catalog on its own. The
// catalog block in the system prompt is the discovery layer; this tool is the
// activation layer — it loads the skill body from disk and returns it as the
// tool result, so activation flows with the turn instead of the host writing
// a permanent system message into the session.
type UseSkill struct {
	catalog []skills.SkillMeta
}

// NewUseSkill builds the use_skill tool bound to the loaded skill catalog.
// cwd is unused: skill bodies are read via each entry's absolute Source path.
func NewUseSkill(cwd string, catalog []skills.SkillMeta) tools.Executable {
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
		ExecutionMode: models.ExecutionSequential,
	}
}

func (t *UseSkill) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	name, _ := args["skill_name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return models.ToolExecutionResult{}, fmt.Errorf("missing skill_name")
	}
	meta, found := skills.FindByName(t.catalog, name)
	if !found {
		return models.ToolExecutionResult{}, fmt.Errorf("unknown skill %q (available: %s)",
			name, t.availableNames())
	}
	skill, err := skills.LoadSkill(meta.Source)
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	text := skills.RenderActiveSkill(skill)
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
	if len(t.catalog) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(t.catalog))
	for _, s := range t.catalog {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

var _ tools.Executable = (*UseSkill)(nil)
