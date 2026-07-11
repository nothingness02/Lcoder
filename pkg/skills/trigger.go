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

// ExpandManualTrigger returns system/user messages that activate a skill.
func ExpandManualTrigger(skill Skill, originalText string) []models.AgentMessage {
	system := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: RenderActiveSkill(skill)})
	user := models.NewAgentMessage(models.RoleUser, models.TextContent{Text: originalText})
	return []models.AgentMessage{system, user}
}
