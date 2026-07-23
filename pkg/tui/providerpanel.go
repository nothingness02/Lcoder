package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

type provStep int

const (
	provStepProvider provStep = iota
	provStepModel
	provStepKey

	provPageSize = 15
)

// providerPanel is the modal provider/model/api-key wizard. It is a plain struct
// (not a tea.Model); the parent Model routes keys to it, mirroring cmdPanel.
type providerPanel struct {
	visible bool
	step    provStep

	providers []config.ProviderInfo
	provIdx   int

	models   []string // model ids for the chosen provider
	modelIdx int

	// manualModel captures a typed model id when the engine returns no models.
	manualModel textinput.Model
	manual      bool

	keyInput textinput.Model

	chosenProvider string
	chosenModel    string
	errMsg         string
	pageSize       int
}

// BuiltinProvidersForPanel exposes the provider list used by the panel (kept as a
// function so tests can assert against the same source).
func BuiltinProvidersForPanel() []config.ProviderInfo {
	return config.BuiltinProviders
}

func newProviderPanel() providerPanel {
	key := textinput.New()
	key.Placeholder = "sk-..."
	key.EchoMode = textinput.EchoPassword
	key.CharLimit = 256

	manual := textinput.New()
	manual.Placeholder = "model-id"
	manual.CharLimit = 128

	return providerPanel{
		step:        provStepProvider,
		providers:   BuiltinProvidersForPanel(),
		keyInput:    key,
		manualModel: manual,
		pageSize:    provPageSize,
	}
}

func (m *Model) openProviderPanel() {
	m.provPanel = newProviderPanel()
	m.provPanel.visible = true
	m.state = stateProvider
}

func (m *Model) closeProviderPanel() {
	m.provPanel = providerPanel{}
	m.state = stateInput
}

// renderProviderPanel returns the overlay body for the current step.
func (m *Model) renderProviderPanel() string {
	p := m.provPanel
	var b strings.Builder
	switch p.step {
	case provStepProvider:
		b.WriteString("Select a provider  (up/down, enter, esc)\n\n")
		for i, pi := range p.providers {
			cursor := "  "
			if i == p.provIdx {
				cursor = lipgloss.NewStyle().Foreground(colorSelect).Render("› ")
			}
			b.WriteString(fmt.Sprintf("%s%s\n", cursor, pi.Display))
		}
	case provStepModel:
		total := len(p.models)
		page := 0
		start := 0
		end := total
		if p.pageSize > 0 && total > 0 {
			page = p.modelIdx / p.pageSize
			start = page * p.pageSize
			if start > total {
				start = 0
				page = 0
			}
			end = start + p.pageSize
			if end > total {
				end = total
			}
		}
		totalPages := 1
		if p.pageSize > 0 {
			totalPages = (total + p.pageSize - 1) / p.pageSize
			if totalPages < 1 {
				totalPages = 1
			}
		}
		b.WriteString(fmt.Sprintf("Select a model for %s  (page %d/%d, up/down, ←/→ page, enter, esc=back)\n\n", p.chosenProvider, page+1, totalPages))
		if p.manual {
			b.WriteString("No models discovered. Type a model id:\n\n")
			b.WriteString(p.manualModel.View())
		} else {
			for i := start; i < end; i++ {
				id := p.models[i]
				cursor := "  "
				if i == p.modelIdx {
					cursor = lipgloss.NewStyle().Foreground(colorSelect).Render("› ")
				}
				b.WriteString(fmt.Sprintf("%s%s\n", cursor, id))
			}
		}
	case provStepKey:
		b.WriteString(fmt.Sprintf("API key for %s / %s  (enter=save, esc=back)\n\n", p.chosenProvider, p.chosenModel))
		b.WriteString(p.keyInput.View())
	}
	if p.errMsg != "" {
		b.WriteString("\n\n")
		b.WriteString(styleError().Render(p.errMsg))
	}
	// Round the whole panel in the same faint box the menus/cmd panel use, so
	// the overlay styling is consistent across surfaces.
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorFaint)
	return box.Render(b.String())
}

// enterModelStep records the chosen provider and loads its model candidates from
// the LLM engine, falling back to the local catalog, then to manual
// entry when neither yields a model for this provider.
func (m *Model) enterModelStep() tea.Cmd {
	p := &m.provPanel
	p.chosenProvider = p.providers[p.provIdx].Name
	p.step = provStepModel
	p.models = nil
	p.modelIdx = 0
	p.manual = false
	p.errMsg = ""

	p.models = m.fetchProviderModels(p.chosenProvider)
	if len(p.models) == 0 {
		p.manual = true
		p.manualModel.SetValue("")
		p.manualModel.Focus()
	}
	return nil
}

// fetchProviderModels returns model ids for the provider: engine discovery first,
// then the local catalog. Returns nil when neither is available.
func (m *Model) fetchProviderModels(provider string) []string {
	var ids []string
	if m.llmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if list, err := m.llmClient.ListModels(ctx); err == nil {
			for _, mi := range list {
				if mi.Provider == provider {
					ids = append(ids, mi.ID)
				}
			}
		} else {
			m.provPanel.errMsg = "拉取模型失败,回退 catalog: " + err.Error()
		}
	}
	if len(ids) == 0 {
		for _, mm := range m.cfg.Catalog.Models {
			if mm.Provider == provider {
				ids = append(ids, mm.ID)
			}
		}
	}
	return ids
}

// enterKeyStep records the chosen model (from the list or manual entry) and moves
// to the api-key step. Returns false when manual entry is empty.
func (m *Model) enterKeyStep() bool {
	p := &m.provPanel
	if p.manual {
		id := strings.TrimSpace(p.manualModel.Value())
		if id == "" {
			return false
		}
		p.chosenModel = id
	} else {
		if len(p.models) == 0 {
			return false
		}
		p.chosenModel = p.models[p.modelIdx]
	}
	p.manualModel.Blur()
	p.step = provStepKey
	p.keyInput.SetValue("")
	p.keyInput.Focus()
	// Prefill an existing key (masked) if one is already configured.
	if conn, ok := m.cfg.Providers[p.chosenProvider]; ok && conn.APIKey != "" {
		p.keyInput.SetValue(conn.APIKey)
	}
	return true
}

// commitProvider persists the entered key, hot-registers the provider with the
// LLM engine, recomputes the context budget for the chosen model, switches the live
// agent, and closes the panel.
func (m *Model) commitProvider() {
	p := &m.provPanel
	provName := p.chosenProvider
	modelID := p.chosenModel
	key := strings.TrimSpace(p.keyInput.Value())

	if m.cfg.Providers == nil {
		m.cfg.Providers = map[string]config.ProviderConn{}
	}

	if key != "" {
		path := config.CredentialsPath()
		creds, _ := config.LoadCredentials(path)
		if creds == nil {
			creds = map[string]config.ProviderConn{}
		}
		entry := creds[provName]
		entry.APIKey = key
		if info, ok := config.BuiltinProvider(provName); ok {
			if entry.Route == "" {
				entry.Route = info.Route
			}
			if entry.BaseURL == "" && info.DefaultBase != "" {
				entry.BaseURL = info.DefaultBase
			}
		}
		creds[provName] = entry
		if err := config.SaveCredentials(path, creds); err != nil {
			p.errMsg = "保存 credentials 失败: " + err.Error()
			return
		}
		m.cfg.Providers[provName] = entry

		if m.llmClient != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.llmClient.RegisterProvider(ctx, provName, entry); err != nil {
				// Non-fatal: the key is saved and will apply on next launch.
				p.errMsg = "引擎热更新失败(下次启动生效): " + err.Error()
			}
		}
	}

	// Recompute the budget for the new model and switch the live agent.
	m.cfg.Provider = provName
	m.cfg.Model = modelID

	// Persist the selection so the next launch uses this provider (whose key is
	// now in credentials.yaml) instead of re-firing the first-launch wizard.
	if err := config.SaveProviderSelection(provName, modelID); err != nil {
		p.errMsg = "保存 config 失败: " + err.Error()
		return
	}

	window := 0
	maxOutput := 0
	if m.llmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		window, _ = m.llmClient.ModelWindow(ctx, provName, modelID)
		maxOutput, _ = m.llmClient.ModelMaxOutput(ctx, provName, modelID)
	}
	budget, _ := m.cfg.ResolveContextBudget(window, maxOutput)
	m.agent.SwitchModel(
		models.ModelRef{Provider: provName, ID: modelID},
		contextmgr.TokenBudget{
			MaxTotal:         budget.MaxTotal,
			TargetTotal:      budget.TargetTotal,
			ReserveOutput:    budget.ReserveOutput,
			MaxOutput:        budget.MaxOutput,
			CompactThreshold: budget.CompactThreshold,
			DropThreshold:    budget.DropThreshold,
			StaticRatio:      m.cfg.Context.StaticRatio,
		},
	)

	m.model = provName + "/" + modelID
	m.header.model = m.model
	m.closeProviderPanel()
}
