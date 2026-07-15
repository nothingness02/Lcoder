package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

// Agent is a loaded subagent definition.
type Agent struct {
	Name        string
	Description string
	Model       string
	Provider    string
	Mode        string
	Timeout     int
	Prompt      string
}

// agentFrontmatter is the YAML frontmatter of an agent markdown file.
type agentFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
	Provider    string `yaml:"provider"`
	Mode        string `yaml:"mode"`
	Timeout     int    `yaml:"timeout"`
}

// DiscoverAgents scans user-level and project-level agent directories.
func DiscoverAgents(projectRoot string) (map[string]Agent, error) {
	agents := make(map[string]Agent)

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
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return Agent{}, fmt.Errorf("agent %s: missing frontmatter", path)
	}
	parts := strings.SplitN(text[4:], "\n---", 2)
	if len(parts) != 2 {
		return Agent{}, fmt.Errorf("agent %s: malformed frontmatter", path)
	}

	var fm agentFrontmatter
	if err := yaml.Unmarshal([]byte(parts[0]), &fm); err != nil {
		return Agent{}, fmt.Errorf("agent %s: unmarshal frontmatter: %w", path, err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return Agent{}, fmt.Errorf("agent %s: name is required", path)
	}

	prompt := ""
	if len(parts) == 2 {
		prompt = strings.TrimSpace(parts[1])
	}

	timeout := fm.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	mode := fm.Mode
	if mode == "" {
		mode = "code"
	}

	return Agent{
		Name:        fm.Name,
		Description: fm.Description,
		Model:       fm.Model,
		Provider:    fm.Provider,
		Mode:        mode,
		Timeout:     timeout,
		Prompt:      prompt,
	}, nil
}
