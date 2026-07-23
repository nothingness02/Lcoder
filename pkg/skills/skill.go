package skills

import (
	"fmt"
	"strings"
)

// UseSkillToolName is the name of the tool the model calls to activate a
// skill on its own. The catalog block (ToCatalogBlock) is the discovery layer;
// the tool is the activation layer.
const UseSkillToolName = "use_skill"

// AllowedToolsDetailsKey is the ToolExecutionResult.Details key through which a
// successful use_skill call publishes the activated skill's tool restriction
// (a []string; empty means unrestricted). The executor consumes it to enforce
// the restriction at execution time.
const AllowedToolsDetailsKey = "skill_allowed_tools"

// SkillMeta is a lightweight catalog entry for a skill. It is loaded eagerly at
// startup and stays in the context window so the agent can discover and match
// skills without paying the token cost of the full skill body.
type SkillMeta struct {
	Name        string
	Description string
	Keywords    []string
	Tags        []string
	// AllowedTools, when non-empty, restricts the agent to these tools while
	// the skill is active. Enforced at execution time by the executor.
	AllowedTools []string
	Source       string // absolute path to SKILL.md
}

// Skill is a fully loaded skill, including the free-form Markdown body that
// follows the YAML frontmatter. It is only loaded on demand.
type Skill struct {
	SkillMeta
	Body string
}

// ToCatalogBlock renders a list of skill metadata for the system prompt. It
// tells the model to activate a matching skill via the use_skill tool, which
// returns the full skill body as a tool result.
func ToCatalogBlock(catalog []SkillMeta) string {
	if len(catalog) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("You have access to the following skills. When the user's request matches a skill's purpose, call the " + UseSkillToolName + " tool with the skill name to load its full instructions before proceeding:\n\n")
	for _, s := range catalog {
		b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		if len(s.Keywords) > 0 {
			b.WriteString(fmt.Sprintf("  keywords: %s\n", strings.Join(s.Keywords, ", ")))
		}
	}
	return b.String()
}

// RenderActiveSkill renders the full content of an activated skill for injection
// into the context window.
func RenderActiveSkill(skill Skill) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("You are now using the %s skill.\n\n", skill.Name))
	if skill.Description != "" {
		b.WriteString("Purpose: ")
		b.WriteString(skill.Description)
		b.WriteString("\n\n")
	}
	if skill.Body != "" {
		b.WriteString(skill.Body)
		b.WriteString("\n\n")
	}
	b.WriteString("Follow the above instructions for the user's request.")
	return b.String()
}
