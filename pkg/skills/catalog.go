package skills

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Scope ranks a skill source; on name conflicts the higher scope wins
// (kimi-code's "Project overrides User overrides Built-in").
type Scope int

const (
	ScopeBuiltin Scope = iota
	ScopeUser
	ScopeUserShared
	ScopeProject
	ScopeProjectShared
)

func (s Scope) label() string {
	switch s {
	case ScopeBuiltin:
		return "Built-in"
	case ScopeUser:
		return "User"
	case ScopeUserShared:
		return "Shared"
	case ScopeProject:
		return "Project"
	case ScopeProjectShared:
		return "Project (shared)"
	default:
		return "Extra"
	}
}

// ScopedMeta is a catalog entry tagged with the scope it came from.
type ScopedMeta struct {
	SkillMeta
	Scope Scope
}

// Source is one skill directory to scan.
type Source struct {
	Scope Scope
	Dir   string // filesystem directory (missing dirs are skipped silently)
	// FSRoot, when non-empty, is an embedded root instead of Dir (e.g.
	// "configs/prompts/skills" inside lcoder.AgentSkills).
	FSRoot string
}

const (
	// catalogBudget caps the rendered catalog block (Kocoro's 4000-char rule).
	catalogBudget = 4000
	// catalogDescMax caps one description (kimi's ~240).
	catalogDescMax = 240
)

// Catalog is the merged, runtime-mutable view of all discovered skills.
type Catalog struct {
	mu       sync.RWMutex
	entries  []ScopedMeta
	disabled map[string]bool
}

// Discover scans the sources and merges them into a catalog. Sources are
// applied low priority first so higher scopes override same-name skills.
func Discover(sources []Source) *Catalog {
	merged := make(map[string]ScopedMeta)
	for _, src := range sources {
		for _, meta := range scanSource(src) {
			key := strings.ToLower(meta.Name)
			merged[key] = ScopedMeta{SkillMeta: meta, Scope: src.Scope}
		}
	}
	entries := make([]ScopedMeta, 0, len(merged))
	for _, e := range merged {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Scope != entries[j].Scope {
			return entries[i].Scope > entries[j].Scope // project scopes first
		}
		return entries[i].Name < entries[j].Name
	})
	return &Catalog{entries: entries, disabled: make(map[string]bool)}
}

// SetDisabled toggles a skill off/on (runtime state; persistence is the
// caller's job).
func (c *Catalog) SetDisabled(name string, off bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if off {
		c.disabled[strings.ToLower(name)] = true
	} else {
		delete(c.disabled, strings.ToLower(name))
	}
}

// IsDisabled reports whether a skill is toggled off.
func (c *Catalog) IsDisabled(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.disabled[strings.ToLower(name)]
}

// Entries returns the merged entries, including disabled ones (the panel
// needs them to show state).
func (c *Catalog) Entries() []ScopedMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]ScopedMeta(nil), c.entries...)
}

// Find looks up a skill by name (case-insensitive).
func (c *Catalog) Find(name string) (ScopedMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := strings.ToLower(name)
	for _, e := range c.entries {
		if strings.ToLower(e.Name) == key {
			return e, true
		}
	}
	return ScopedMeta{}, false
}

// Block renders the catalog for the system prompt: grouped by scope, budget
// capped, hidden and disabled skills excluded (kimi-code's listing shape).
func (c *Catalog) Block() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b strings.Builder
	b.WriteString("You have access to the following skills. When the user's request matches a skill's purpose, call the " +
		UseSkillToolName + " tool with the skill name to load its full instructions before proceeding. " +
		"On name conflicts, Project overrides User overrides Built-in.\n")

	budget := catalogBudget - b.Len()
	truncated := 0
	var truncatedNames []string

	// Highest-priority scope first: project skills are the most relevant and
	// are the last to be sacrificed when the budget runs out.
	visible := make([]ScopedMeta, 0, len(c.entries))
	for _, e := range c.entries {
		// Sub-skills (parent.child) are reached through their parent's
		// instructions, not the top-level listing (kimi-code's rule).
		if e.Hidden || e.IsSubSkill || c.disabled[strings.ToLower(e.Name)] {
			continue
		}
		visible = append(visible, e)
	}

	lastScope := Scope(-1)
	for _, e := range visible {
		var sb strings.Builder
		if e.Scope != lastScope {
			fmt.Fprintf(&sb, "\n### %s\n", e.Scope.label())
			lastScope = e.Scope
		}
		desc := truncateRunes(e.Description, catalogDescMax)
		fmt.Fprintf(&sb, "- %s: %s\n", e.Name, desc)
		if sb.Len() > budget {
			truncated++
			truncatedNames = append(truncatedNames, e.Name)
			continue
		}
		b.WriteString(sb.String())
		budget -= sb.Len()
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "\n… +%d more skills (%s)\n", truncated, strings.Join(truncatedNames, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncateRunes clips s to at most n runes on a rune boundary.
func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// NewCatalog wraps explicit entries into a Catalog (tests, custom sources).
func NewCatalog(entries []ScopedMeta) *Catalog {
	return &Catalog{entries: entries, disabled: make(map[string]bool)}
}
