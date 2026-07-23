package skills

import (
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// FindByName looks up a skill by name in a loaded skill catalog.
func FindByName(catalog []SkillMeta, name string) (SkillMeta, bool) {
	for _, s := range catalog {
		if s.Name == name {
			return s, true
		}
	}
	return SkillMeta{}, false
}

// ParseManualTrigger checks if text is a manual skill trigger like "/skill:name".
// It returns the skill name and the remaining user text (if any).
func ParseManualTrigger(text string) (name string, rest string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/skill:") {
		return "", text, false
	}
	after := strings.TrimPrefix(text, "/skill:")
	parts := strings.SplitN(after, " ", 2)
	name = strings.TrimSpace(parts[0])
	if name == "" {
		return "", text, false
	}
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}
	return name, rest, true
}

// ExpandManualTrigger folds an activated skill into a single user message: the
// skill instructions followed by the user's request. Activation no longer
// writes a permanent system message into the session — on the model-driven
// path the same body arrives as a use_skill tool result.
func ExpandManualTrigger(skill Skill, originalText string) models.AgentMessage {
	text := RenderActiveSkill(skill)
	if strings.TrimSpace(originalText) != "" {
		text += "\n\nUser request: " + originalText
	}
	return models.NewAgentMessage(models.RoleUser, models.TextContent{Text: text})
}
