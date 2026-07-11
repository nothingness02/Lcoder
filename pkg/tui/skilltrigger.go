package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/skills"
)

// handleSkillTrigger activates a named skill and starts the agent on the
// expanded prompt. Ported from the pre-rewrite TUI so manual `/skill:name`
// triggers and auto-detection keep working under the state-machine model.
func (m *Model) handleSkillTrigger(name, rest string) tea.Cmd {
	meta, found := skills.FindByName(m.skills, name)
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

	expanded := skills.ExpandManualTrigger(skill, rest)
	if len(expanded) == 0 {
		m.addSystem(styleError().Render(fmt.Sprintf("skill %q produced no messages", name)))
		return nil
	}

	m.addSystem(styleDim().Render("activated skill: " + skill.Name))
	for _, msg := range expanded {
		if err := m.session.Append(msg); err != nil {
			m.addSystem(styleError().Render(err.Error()))
			return nil
		}
	}

	// The last expanded message is the user request; use it to start the run.
	last := expanded[len(expanded)-1]
	return m.startPrompt(last.Text())
}

// autoDetectEnabled reports whether plain prompts should be screened for a
// matching skill. The TUI has no direct access to config; default to enabled.
func (m *Model) autoDetectEnabled() bool { return true }

// availableSkillNames lists the loaded skills for error messages.
func (m *Model) availableSkillNames() string {
	if len(m.skills) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(m.skills))
	for _, s := range m.skills {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
