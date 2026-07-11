package skills

import (
	"fmt"
	"strings"
)

// SkillMeta is a lightweight catalog entry for a skill. It is loaded eagerly at
// startup and stays in the context window so the agent can discover and match
// skills without paying the token cost of the full skill body.
type SkillMeta struct {
	Name        string
	Description string
	Keywords    []string
	Tags        []string
	Source      string // absolute path to SKILL.md
}

// Skill is a fully loaded skill, including the free-form Markdown body that
// follows the YAML frontmatter. It is only loaded on demand.
type Skill struct {
	SkillMeta
	Body string
}

// ToCatalogBlock renders a list of skill metadata for the system prompt.
func ToCatalogBlock(catalog []SkillMeta) string {
	if len(catalog) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("You have access to the following skills. Use them when appropriate:\n\n")
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
