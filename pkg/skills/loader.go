package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder"
	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

// DefaultSources returns the skill sources in ascending priority order
// (builtin < user < user-shared < project < project-shared), with optional
// extra directories appended at user scope.
func DefaultSources(cwd string, extraDirs []string) []Source {
	sources := []Source{
		{Scope: ScopeBuiltin, FSRoot: "configs/prompts/skills"},
		{Scope: ScopeUser, Dir: paths.LCoderHome("skills")},
		{Scope: ScopeUserShared, Dir: filepath.Join(paths.HomeDir(), ".agents", "skills")},
		{Scope: ScopeProject, Dir: filepath.Join(cwd, ".lcoder", "skills")},
		{Scope: ScopeProjectShared, Dir: filepath.Join(cwd, ".agents", "skills")},
	}
	for _, dir := range extraDirs {
		if dir == "" {
			continue
		}
		if strings.HasPrefix(dir, "~/") {
			dir = paths.LCoderHome(dir[2:])
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		sources = append(sources, Source{Scope: ScopeUser, Dir: dir})
	}
	return sources
}

// DefaultPaths returns the default skill search paths relative to cwd and home.
// Deprecated: kept for callers that still pass plain path lists.
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

// LoadSkill reads a full SKILL.md and returns the complete skill including
// its free-form Markdown body. Embedded builtin skills (Source paths under
// configs/prompts/skills) are read from the embedded FS when the file is absent.
func LoadSkill(source string) (Skill, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		if strings.HasPrefix(source, "configs/prompts/skills/") {
			data, err = lcoder.AgentSkills.ReadFile(source)
		}
		if err != nil {
			return Skill{}, fmt.Errorf("read skill %s: %w", source, err)
		}
	}
	return parse(data, source)
}

type frontMatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Keywords     []string `yaml:"keywords"`
	Tags         []string `yaml:"tags"`
	AllowedTools []string `yaml:"allowed_tools"`
	Hidden       bool     `yaml:"hidden"`
	// Both spellings are accepted (kimi-code compatibility).
	DisableModelInvocation  bool `yaml:"disableModelInvocation"`
	DisableModelInvocation2 bool `yaml:"disable-model-invocation"`
	// HasSubSkill allows the discovery to recurse into this skill's
	// directory for nested child skills (parent.child).
	HasSubSkill  bool `yaml:"has-sub-skill"`
	HasSubSkill2 bool `yaml:"hasSubSkill"`
}

func (fm frontMatter) disableModel() bool {
	return fm.DisableModelInvocation || fm.DisableModelInvocation2
}

func (fm frontMatter) hasSubSkill() bool {
	return fm.HasSubSkill || fm.HasSubSkill2
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
		Name:                   fm.Name,
		Description:            fm.Description,
		Keywords:               fm.Keywords,
		Tags:                   fm.Tags,
		AllowedTools:           fm.AllowedTools,
		Hidden:                 fm.Hidden,
		DisableModelInvocation: fm.disableModel(),
		HasSubSkill:            fm.hasSubSkill(),
		Source:                 source,
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
		Name:                   fm.Name,
		Description:            fm.Description,
		Keywords:               fm.Keywords,
		Tags:                   fm.Tags,
		AllowedTools:           fm.AllowedTools,
		Hidden:                 fm.Hidden,
		DisableModelInvocation: fm.disableModel(),
		HasSubSkill:            fm.hasSubSkill(),
		Source:                 source,
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

func tokenize(text string) []string {
	replacer := strings.NewReplacer(
		",", " ",
		".", " ",
		":", " ",
		";", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"{", " ",
		"}", " ",
	)
	text = replacer.Replace(text)
	var tokens []string
	for _, p := range strings.Fields(text) {
		tokens = append(tokens, strings.ToLower(p))
	}
	return tokens
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

// parseMetaBytes parses a SKILL.md's frontmatter from bytes (embedded skills).
func parseMetaBytes(name string, data []byte) (SkillMeta, error) {
	fm, _, err := splitFrontMatter(data)
	if err != nil {
		return SkillMeta{}, err
	}
	meta := SkillMeta{
		Name:                   fm.Name,
		Description:            fm.Description,
		Keywords:               fm.Keywords,
		Tags:                   fm.Tags,
		AllowedTools:           fm.AllowedTools,
		Hidden:                 fm.Hidden,
		DisableModelInvocation: fm.disableModel(),
		Source:                 name,
	}
	if len(meta.Keywords) == 0 {
		meta.Keywords = deriveKeywords(meta.Name, meta.Description)
	}
	return meta, nil
}

// scanSource lists a skill source: a filesystem directory, or the embedded
// builtin root when FSRoot is set.
func scanSource(src Source) []SkillMeta {
	if src.FSRoot != "" {
		return scanEmbedded(src.FSRoot)
	}
	return scanDir(src.Dir)
}

// maxSkillScanDepth bounds sub-skill nesting (kimi-code's MAX_SKILL_SCAN_DEPTH).
const maxSkillScanDepth = 8

// scanDir lists a skill directory, recursing into packages that declare
// has-sub-skill and namespacing their children as parent.child.
func scanDir(base string) []SkillMeta {
	return scanDirDepth(base, "", 0)
}

func scanDirDepth(base, parentName string, depth int) []SkillMeta {
	if depth > maxSkillScanDepth {
		return nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []SkillMeta
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		meta, err := parseMeta(filepath.Join(base, name, "SKILL.md"))
		if err != nil {
			continue
		}
		if meta.Name == "" {
			meta.Name = name
		}
		if parentName != "" {
			meta.Name = parentName + "." + meta.Name
			meta.IsSubSkill = true
		}
		out = append(out, meta)
		if meta.HasSubSkill {
			out = append(out, scanDirDepth(filepath.Join(base, name), meta.Name, depth+1)...)
		}
	}
	return out
}

func scanEmbedded(root string) []SkillMeta {
	entries, err := lcoder.AgentSkills.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []SkillMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := lcoder.AgentSkills.ReadFile(root + "/" + entry.Name() + "/SKILL.md")
		if err != nil {
			continue
		}
		meta, err := parseMetaBytes(root+"/"+entry.Name(), data)
		if err != nil {
			continue
		}
		if meta.Name == "" {
			meta.Name = entry.Name()
		}
		out = append(out, meta)
	}
	return out
}
