package tui

import (
	"strings"

	"github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/config"
)

// handleThinkingCommand implements the /thinking command family.
//
//	/thinking                  → open the horizontal effort picker
//	/thinking low              → apply session-only, no persistence
//	/thinking low --persist    → apply session-only + write config.yaml
//	/thinking off              → disable (rejected for AlwaysThinking models)
func (m *Model) handleThinkingCommand(args string) tea.Cmd {
	persist := false
	arg := strings.TrimSpace(args)
	if strings.HasSuffix(arg, "--persist") {
		persist = true
		arg = strings.TrimSpace(strings.TrimSuffix(arg, "--persist"))
	}

	if arg == "" {
		m.openEffortPicker()
		return nil
	}

	m.applyThinking(arg, persist)
	return nil
}

// openEffortPicker builds the horizontal segment selector from the model's
// declared efforts and shows it above the composer.
func (m *Model) openEffortPicker() {
	prov, model := m.thinkingTarget()
	if prov == "" || model == "" {
		m.showTextPanel("thinking", styleDim().Render("no active model — set one via /provider first"))
		return
	}
	var efforts []string
	offAvailable := true
	if m.llmClient != nil {
		efforts, offAvailable = m.llmClient.ThinkingEfforts(nil, prov, model)
	}
	if len(efforts) == 0 {
		// No declared efforts: fall back to the generic toggle pair.
		efforts = []string{"on"}
		if offAvailable {
			efforts = append([]string{"off"}, efforts...)
		}
	} else if offAvailable {
		// The model declares efforts and can be disabled: offer off first.
		efforts = append([]string{"off"}, efforts...)
	}
	m.effortSel = newEffortSelector(efforts, m.agent.Thinking(), m.effortWarning())
	m.updateSizes()
}

// applyThinking validates the requested effort, applies it to the live agent,
// optionally persists to config.yaml, and reports the result.
func (m *Model) applyThinking(effort string, persist bool) {
	prov, model := m.thinkingTarget()
	resolved, warning := "", ""
	if m.llmClient != nil && prov != "" && model != "" {
		resolved, warning = m.llmClient.ResolveThinking(nil, prov, model, effort)
	} else {
		resolved = effort
	}
	if resolved == "" {
		msg := warning
		if msg == "" {
			msg = "thinking adjustment rejected"
		}
		m.showTextPanel("thinking", styleError().Render(msg))
		return
	}
	m.agent.SwitchThinking(resolved)
	if persist {
		if err := configSaveThinking(resolved); err != nil {
			m.showTextPanel("thinking", styleError().Render("persist failed: "+err.Error()))
			return
		}
	}
	text := "thinking: " + resolved
	if warning != "" {
		text += "\n" + styleWarn().Render(warning)
	}
	if w := m.effortWarning(); w != "" {
		text += "\n" + styleWarn().Render(w)
	}
	m.showTextPanel("thinking", text)
}

// applyEffortSelection commits the picker's active segment. persist=true for
// Enter (writes config.yaml), false for Alt+S (session only).
func (m *Model) applyEffortSelection(persist bool) {
	if m.effortSel == nil {
		return
	}
	effort := m.effortSel.selected()
	m.effortSel = nil
	m.updateSizes()
	if effort == "" {
		return
	}
	m.applyThinking(effort, persist)
}

// effortWarning returns the cache-invalidation notice once the conversation
// has history (mirrors kimi-code's EFFORT_SWITCH_CACHE_WARNING).
func (m *Model) effortWarning() string {
	if m.completedTurns > 0 {
		return "switching thinking effort may invalidate the prompt cache"
	}
	return ""
}

// thinkingTarget returns the provider/model pair for thinking resolution.
// Prefers the live cfg (kept in sync by the provider wizard), falling back to
// parsing the m.model display string ("prov/model").
func (m *Model) thinkingTarget() (prov, model string) {
	if m.cfg.Provider != "" && m.cfg.Model != "" {
		return m.cfg.Provider, m.cfg.Model
	}
	if i := strings.Index(m.model, "/"); i > 0 {
		return m.model[:i], m.model[i+1:]
	}
	return "", ""
}

// configSaveThinking indirection keeps the TUI free of a direct config import
// cycle and is trivially stubbable in tests.
var configSaveThinking = configSaveThinkingImpl

func configSaveThinkingImpl(thinking string) error {
	return config.SaveThinking(thinking)
}
