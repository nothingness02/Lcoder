package lcoder

import "embed"

// AgentModes contains the built-in agent mode YAML files shipped with Lcoder.
// These are embedded at build time so the binary carries its default modes
// regardless of the working directory.
//
//go:embed configs/agents/*.yaml
var AgentModes embed.FS
