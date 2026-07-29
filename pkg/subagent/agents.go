package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder"
	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

const (
	defaultAgentMode       = "code"
	defaultAgentTimeoutSec = 120
)

// Agent is a loaded subagent profile: a self-contained definition of what a
// spawned subagent is (prompt + mode/tool surface + budget + summary policy).
type Agent struct {
	Name        string
	Description string
	Model       string // model id; empty means inherit the parent's model
	Provider    string
	Mode        string
	Timeout     int // wall-clock seconds per run
	MaxTurns    int // turn budget; 0 = unlimited
	// SummaryMinChars/SummaryRetries form the summary floor: when the final
	// answer is shorter than SummaryMinChars, the subagent is asked to expand
	// it, up to SummaryRetries times. 0 disables the floor.
	SummaryMinChars int
	SummaryRetries  int
	Subagents       []string // profile names this agent may delegate to; empty = none
	Prompt          string
}

// agentFrontmatter is the YAML frontmatter of an agent markdown file.
type agentFrontmatter struct {
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	Model           string   `yaml:"model"`
	Provider        string   `yaml:"provider"`
	Mode            string   `yaml:"mode"`
	Timeout         int      `yaml:"timeout"`
	MaxTurns        int      `yaml:"max_turns"`
	SummaryMinChars int      `yaml:"summary_min_chars"`
	SummaryRetries  int      `yaml:"summary_retries"`
	Subagents       []string `yaml:"subagents"`
}

// DefaultProfiles loads the built-in subagent profiles embedded from
// configs/agents/*.md. User and project agent files override these by name
// (same convention as mode loading). Files prefixed with "_" are shared
// fragments (e.g. the role prefix), not profiles.
func DefaultProfiles() map[string]Agent {
	agents := make(map[string]Agent)
	entries, err := lcoder.AgentProfiles.ReadDir("configs/agents")
	if err != nil {
		return agents
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, "_") {
			continue
		}
		data, err := lcoder.AgentProfiles.ReadFile("configs/agents/" + name)
		if err != nil {
			continue
		}
		profile, err := parseAgentMarkdown("configs/agents/"+name, data)
		if err != nil {
			continue
		}
		agents[profile.Name] = profile
	}
	return agents
}

// DiscoverAgents scans user-level and project-level agent directories.
// Built-in defaults are overlaid by user then project files, by name.
func DiscoverAgents(projectRoot string) (map[string]Agent, error) {
	agents := DefaultProfiles()

	userDir := paths.LCoderHome("agents")
	if err := loadAgentsFromDir(userDir, agents); err != nil {
		return nil, err
	}

	if projectRoot != "" {
		projectDir := filepath.Join(projectRoot, ".lcoder", "agents")
		if err := loadAgentsFromDir(projectDir, agents); err != nil {
			return nil, err
		}
	}

	return agents, nil
}

func loadAgentsFromDir(dir string, out map[string]Agent) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read agent dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read agent file %s: %w", path, err)
		}
		agent, err := parseAgentMarkdown(path, data)
		if err != nil {
			return fmt.Errorf("parse agent file %s: %w", path, err)
		}
		out[agent.Name] = agent
	}
	return nil
}

func parseAgentMarkdown(path string, data []byte) (Agent, error) {
	// Normalize CRLF so a standalone "---" line is recognized regardless of
	// line ending style.
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Agent{}, fmt.Errorf("agent %s: missing frontmatter", path)
	}

	lines := strings.Split(text, "\n")
	var fmLines []string
	var bodyLines []string
	foundClosing := false
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			foundClosing = true
			bodyLines = lines[i+1:]
			break
		}
		fmLines = append(fmLines, lines[i])
	}
	if !foundClosing {
		return Agent{}, fmt.Errorf("agent %s: malformed frontmatter", path)
	}

	var fm agentFrontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(fmLines, "\n")), &fm); err != nil {
		return Agent{}, fmt.Errorf("agent %s: unmarshal frontmatter: %w", path, err)
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return Agent{}, fmt.Errorf("agent %s: name is required", path)
	}

	prompt := strings.TrimSpace(strings.Join(bodyLines, "\n"))

	timeout := fm.Timeout
	if timeout <= 0 {
		timeout = defaultAgentTimeoutSec
	}
	mode := fm.Mode
	if mode == "" {
		mode = defaultAgentMode
	}

	return Agent{
		Name:            name,
		Description:     fm.Description,
		Model:           fm.Model,
		Provider:        fm.Provider,
		Mode:            mode,
		Timeout:         timeout,
		MaxTurns:        fm.MaxTurns,
		SummaryMinChars: fm.SummaryMinChars,
		SummaryRetries:  fm.SummaryRetries,
		Subagents:       fm.Subagents,
		Prompt:          prompt,
	}, nil
}
