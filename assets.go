package lcoder

import "embed"

// AgentModes contains the built-in agent mode YAML files shipped with Lcoder.
// These are embedded at build time so the binary carries its default modes
// regardless of the working directory.
//
//go:embed configs/prompts/modes/*.yaml
var AgentModes embed.FS

// SystemPromptMD is the built-in base system prompt (markdown), embedded so
// single-file installs always have it; files on disk override it (see
// agentsetup.BuildSystemPrompt).
//
//go:embed configs/prompts/system.md
var SystemPromptMD string

// AgentProfiles contains the built-in subagent profiles (markdown with YAML
// frontmatter) and shared prompt fragments (files prefixed with "_" are
// fragments, not profiles).
//
//go:embed configs/prompts/agents/*.md
var AgentProfiles embed.FS

// AgentSkills contains the built-in skills shipped with Lcoder (each a
// directory with a SKILL.md). They are the lowest-priority skill scope;
// user and project directories override them by name.
//
//go:embed configs/prompts/skills
var AgentSkills embed.FS
