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
	// Hidden excludes the skill from the model-facing catalog (manual
	// /skill:name activation still works).
	Hidden bool
	// DisableModelInvocation blocks model-driven use_skill calls entirely
	// (manual activation only).
	DisableModelInvocation bool
	// HasSubSkill allows recursion into this skill's directory for nested
	// children (kimi-code's has-sub-skill).
	HasSubSkill bool
	// IsSubSkill marks a nested child skill (parent.child). Sub-skills are
	// excluded from the model-facing catalog but can be activated by name.
	IsSubSkill bool
	Source     string // absolute path to SKILL.md
}

// Skill is a fully loaded skill, including the free-form Markdown body that
// follows the YAML frontmatter. It is only loaded on demand.
type Skill struct {
	SkillMeta
	Body string
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
