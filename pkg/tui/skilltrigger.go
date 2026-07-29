package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/skills"
)

// handleSkillTrigger activates a named skill and starts the agent on the
// expanded prompt. The skill body is folded into the user message — the same
// content the model-driven path receives as a use_skill tool result — so no
// permanent system message is written into the session.
func (m *Model) handleSkillTrigger(name, rest string) tea.Cmd {
	meta, found := m.skills.Find(name)
	if !found {
		m.addSystem(styleError().Render(
			fmt.Sprintf("skill %q not found. available: %s", name, m.availableSkillNames())))
		return nil
	}

	skill, err := skills.LoadSkill(meta.Source)
	if err != nil {
		m.addSystem(styleError().Render(fmt.Sprintf("load skill %q: %v", name, err)))
		return nil
	}

	m.addSystem(styleInfo().Render("activated skill: " + skill.Name))
	// startPrompt appends the expanded message to the session and submits it.
	return m.startPrompt(skills.ExpandManualTrigger(skill, rest).Text())
}

// availableSkillNames lists the loaded skills for error messages.
func (m *Model) availableSkillNames() string {
	entries := m.skills.Entries()
	if len(entries) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return strings.Join(names, ", ")
}
