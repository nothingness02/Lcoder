package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

// DefaultPaths returns the default skill search paths relative to cwd and home.
func DefaultPaths(cwd string) []string {
	var out []string
	out = append(out, paths.LCoderHome("skills"))
	out = append(out, filepath.Join(cwd, ".lcoder", "skills"))
	out = append(out, filepath.Join(cwd, ".agents", "skills"))
	return out
}

// Load discovers and parses skills from the given directories.
func Load(paths []string) ([]Skill, error) {
	var skills []Skill
	seen := make(map[string]bool)

	for _, base := range paths {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(base, entry.Name(), "SKILL.md")
			data, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			skill, err := parse(data)
			if err != nil {
				continue
			}
			if skill.Name == "" {
				skill.Name = entry.Name()
			}
			skill.Source = skillPath
			if !seen[skill.Name] {
				seen[skill.Name] = true
				skills = append(skills, skill)
			}
		}
	}
	return skills, nil
}

type frontMatter struct {
	Name         string   `yaml:"name"`
	WhenToUse    string   `yaml:"when_to_use"`
	Steps        []string `yaml:"steps"`
	Examples     []string `yaml:"examples"`
	OutputFormat string   `yaml:"output_format"`
}

func parse(data []byte) (Skill, error) {
	content := string(data)
	var fm frontMatter

	if strings.HasPrefix(content, "---") {
		end := strings.Index(content[3:], "---")
		if end != -1 {
			if err := yaml.Unmarshal([]byte(content[3:end+3]), &fm); err != nil {
				return Skill{}, fmt.Errorf("invalid frontmatter: %w", err)
			}
			content = strings.TrimSpace(content[end+6:])
		}
	}

	sections := parseSections(content)

	if len(fm.Steps) == 0 {
		if steps, ok := sections["Steps"]; ok {
			fm.Steps = splitLines(steps)
		}
	}
	if len(fm.Examples) == 0 {
		if examples, ok := sections["Examples"]; ok {
			fm.Examples = splitLines(examples)
		}
	}
	if fm.OutputFormat == "" {
		fm.OutputFormat = sections["Output Format"]
	}
	if fm.WhenToUse == "" {
		fm.WhenToUse = firstParagraph(content)
	}

	return Skill{
		Name:         fm.Name,
		WhenToUse:    fm.WhenToUse,
		Steps:        fm.Steps,
		Examples:     fm.Examples,
		OutputFormat: fm.OutputFormat,
	}, nil
}

func parseSections(content string) map[string]string {
	sections := make(map[string]string)
	lines := strings.Split(content, "\n")
	var current string
	var buffer strings.Builder

	flush := func() {
		if current != "" {
			sections[current] = strings.TrimSpace(buffer.String())
		}
		buffer.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			continue
		}
		buffer.WriteString(line)
		buffer.WriteByte('\n')
	}
	flush()
	return sections
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func firstParagraph(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "#") {
		idx := strings.Index(content, "\n")
		if idx != -1 {
			content = strings.TrimSpace(content[idx+1:])
		}
	}
	idx := strings.Index(content, "\n\n")
	if idx != -1 {
		return strings.TrimSpace(content[:idx])
	}
	return strings.TrimSpace(content)
}
