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

// LoadCatalog discovers skill directories and parses only the YAML frontmatter
// of each SKILL.md, returning lightweight metadata suitable for eager loading.
func LoadCatalog(paths []string) ([]SkillMeta, error) {
	var catalog []SkillMeta
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
			meta, err := parseMeta(skillPath)
			if err != nil {
				continue
			}
			if meta.Name == "" {
				meta.Name = entry.Name()
			}
			if !seen[meta.Name] {
				seen[meta.Name] = true
				catalog = append(catalog, meta)
			}
		}
	}
	return catalog, nil
}

// LoadSkill reads a full SKILL.md file from disk and returns the complete skill
// including its free-form Markdown body.
func LoadSkill(source string) (Skill, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %s: %w", source, err)
	}
	return parse(data, source)
}

type frontMatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Keywords    []string `yaml:"keywords"`
	Tags        []string `yaml:"tags"`
}

func parseMeta(source string) (SkillMeta, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return SkillMeta{}, err
	}
	fm, body, err := splitFrontMatter(data)
	if err != nil {
		return SkillMeta{}, err
	}
	_ = body
	meta := SkillMeta{
		Name:        fm.Name,
		Description: fm.Description,
		Keywords:    fm.Keywords,
		Tags:        fm.Tags,
		Source:      source,
	}
	if len(meta.Keywords) == 0 {
		meta.Keywords = deriveKeywords(meta.Name, meta.Description)
	}
	return meta, nil
}

func parse(data []byte, source string) (Skill, error) {
	fm, body, err := splitFrontMatter(data)
	if err != nil {
		return Skill{}, err
	}
	meta := SkillMeta{
		Name:        fm.Name,
		Description: fm.Description,
		Keywords:    fm.Keywords,
		Tags:        fm.Tags,
		Source:      source,
	}
	if len(meta.Keywords) == 0 {
		meta.Keywords = deriveKeywords(meta.Name, meta.Description)
	}
	return Skill{
		SkillMeta: meta,
		Body:      body,
	}, nil
}

func splitFrontMatter(data []byte) (frontMatter, string, error) {
	content := string(data)
	var fm frontMatter

	if strings.HasPrefix(content, "---") {
		end := strings.Index(content[3:], "---")
		if end != -1 {
			if err := yaml.Unmarshal([]byte(content[3:end+3]), &fm); err != nil {
				return fm, "", fmt.Errorf("invalid frontmatter: %w", err)
			}
			content = strings.TrimSpace(content[end+6:])
		}
	}
	return fm, content, nil
}

func deriveKeywords(name, description string) []string {
	var tokens []string
	tokens = append(tokens, tokenize(strings.ToLower(name))...)
	tokens = append(tokens, tokenize(strings.ToLower(description))...)
	seen := make(map[string]bool)
	var keywords []string
	for _, t := range tokens {
		if len(t) > 3 && !seen[t] {
			seen[t] = true
			keywords = append(keywords, t)
		}
	}
	return keywords
}
