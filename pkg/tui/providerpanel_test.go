package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
)

func TestOpenProviderPanelShowsProviderStep(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.openProviderPanel()

	if m.state != stateProvider {
		t.Fatalf("expected stateProvider, got %v", m.state)
	}
	if !m.provPanel.visible {
		t.Fatal("expected panel visible")
	}
	if m.provPanel.step != provStepProvider {
		t.Fatalf("expected provStepProvider, got %v", m.provPanel.step)
	}
	if len(m.provPanel.providers) != len(BuiltinProvidersForPanel()) {
		t.Fatalf("expected %d providers, got %d", len(BuiltinProvidersForPanel()), len(m.provPanel.providers))
	}
}

func TestProviderStepNavigationAndEsc(t *testing.T) {
	m, _, _ := newTestModel()
	m.openProviderPanel()

	// Down moves the selection.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.provPanel.provIdx != 1 {
		t.Fatalf("expected provIdx 1 after down, got %d", m.provPanel.provIdx)
	}

	// Up at top clamps to 0.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.provPanel.provIdx != 0 {
		t.Fatalf("expected provIdx 0 (clamped), got %d", m.provPanel.provIdx)
	}

	// Esc closes the panel back to input.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.provPanel.visible || m.state != stateInput {
		t.Fatalf("expected closed panel + stateInput, got visible=%v state=%v", m.provPanel.visible, m.state)
	}
}

func TestModelStepFetchFiltersByProvider(t *testing.T) {
	m, _, _ := newTestModel()
	m.llmClient = llmtest.Client()
	m.openProviderPanel()
	// Provider 0 is openai per BuiltinProviders order.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.provPanel.step != provStepModel {
		t.Fatalf("expected provStepModel, got %v", m.provPanel.step)
	}
	if len(m.provPanel.models) == 0 {
		t.Fatal("expected at least one openai model")
	}
	if !slices.Contains(m.provPanel.models, "gpt-4o") {
		t.Fatalf("expected openai models to include gpt-4o, got %v", m.provPanel.models)
	}
}

func TestModelStepManualFallbackWhenEmpty(t *testing.T) {
	m, _, _ := newTestModel()
	m.llmClient = nil       // no discovery source
	m.cfg = config.Config{} // no catalog fallback either
	m.openProviderPanel()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.provPanel.manual {
		t.Fatal("expected manual model entry when no models discovered")
	}
}

func TestModelStepEnterAdvancesToKey(t *testing.T) {
	m, _, _ := newTestModel()
	m.llmClient = llmtest.Client()
	m.openProviderPanel()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // provider -> model
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model -> key

	if m.provPanel.step != provStepKey {
		t.Fatalf("expected provStepKey, got %v", m.provPanel.step)
	}
	if m.provPanel.chosenModel != m.provPanel.models[0] {
		t.Fatalf("expected chosenModel %q, got %q", m.provPanel.models[0], m.provPanel.chosenModel)
	}
}

func TestCommitProviderSavesRegistersAndSwitches(t *testing.T) {
	m, agent, _ := newTestModel()
	m.llmClient = llmtest.Client()
	// Persist credentials to a temp HOME so we do not touch the real file.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	m.openProviderPanel()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // provider -> model
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model -> key
	chosenModel := m.provPanel.chosenModel
	// Type a key and submit.
	for _, r := range "sk-test" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit

	if m.state != stateInput || m.provPanel.visible {
		t.Fatalf("expected panel closed after commit, state=%v visible=%v", m.state, m.provPanel.visible)
	}
	if agent.SwitchedModel.Provider != "openai" || agent.SwitchedModel.ID != chosenModel {
		t.Fatalf("expected agent switched to openai/%s, got %+v", chosenModel, agent.SwitchedModel)
	}
	window, _ := m.llmClient.ModelWindow(context.Background(), "openai", chosenModel)
	maxOutput, _ := m.llmClient.ModelMaxOutput(context.Background(), "openai", chosenModel)
	expected, _ := m.cfg.ResolveContextBudget(window, maxOutput)
	if agent.SwitchedBudget.MaxTotal != expected.MaxTotal {
		t.Fatalf("expected budget MaxTotal %d from catalog window, got %d", expected.MaxTotal, agent.SwitchedBudget.MaxTotal)
	}
	if m.model != "openai/"+chosenModel {
		t.Fatalf("expected display model openai/%s, got %q", chosenModel, m.model)
	}
}

func TestSlashProviderOpensPanel(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	m.dispatchSlash("/provider")

	if m.state != stateProvider || !m.provPanel.visible {
		t.Fatalf("expected provider panel open, state=%v visible=%v", m.state, m.provPanel.visible)
	}
}

func TestFirstLaunchAutoOpensPanel(t *testing.T) {
	bus := events.New()
	store := &fakeSessionStore{}
	m := NewModel(bus, &fakeAgent{}, &fakeSession{ID: "x"}, store, ".", "x",
		"openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, true /* needsProviderSetup */, nil)
	defer m.Close()

	if m.state != stateProvider || !m.provPanel.visible {
		t.Fatalf("expected wizard auto-open on first launch, state=%v visible=%v", m.state, m.provPanel.visible)
	}
}

func TestModelStepPaginationRendersFifteenPerPage(t *testing.T) {
	m, _, _ := newTestModel()
	m.openProviderPanel()
	m.provPanel.step = provStepModel
	m.provPanel.chosenProvider = "openai"
	for i := 0; i < 22; i++ {
		m.provPanel.models = append(m.provPanel.models, fmt.Sprintf("model-%02d", i))
	}
	m.provPanel.modelIdx = 0

	out := m.renderProviderPanel()
	if !strings.Contains(out, "page 1/2") {
		t.Fatalf("expected page indicator 1/2, got:\n%s", out)
	}
	for i := 0; i < 15; i++ {
		if !strings.Contains(out, fmt.Sprintf("model-%02d", i)) {
			t.Fatalf("expected first-page model model-%02d in render, got:\n%s", i, out)
		}
	}
	for i := 15; i < 22; i++ {
		if strings.Contains(out, fmt.Sprintf("model-%02d", i)) {
			t.Fatalf("did not expect second-page model model-%02d on first page, got:\n%s", i, out)
		}
	}
}

func TestModelStepPageDownAndPageUp(t *testing.T) {
	m, _, _ := newTestModel()
	m.openProviderPanel()
	m.provPanel.step = provStepModel
	m.provPanel.chosenProvider = "openai"
	for i := 0; i < 22; i++ {
		m.provPanel.models = append(m.provPanel.models, fmt.Sprintf("model-%02d", i))
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.provPanel.modelIdx != 15 {
		t.Fatalf("expected modelIdx 15 after page right, got %d", m.provPanel.modelIdx)
	}
	out := m.renderProviderPanel()
	if !strings.Contains(out, "page 2/2") {
		t.Fatalf("expected page indicator 2/2, got:\n%s", out)
	}
	if !strings.Contains(out, "model-15") {
		t.Fatalf("expected second page to contain model-15, got:\n%s", out)
	}
	if strings.Contains(out, "model-00") {
		t.Fatalf("did not expect model-00 on second page, got:\n%s", out)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.provPanel.modelIdx != 0 {
		t.Fatalf("expected modelIdx 0 after page left, got %d", m.provPanel.modelIdx)
	}
	if !strings.Contains(m.renderProviderPanel(), "page 1/2") {
		t.Fatalf("expected page indicator 1/2 after paging back")
	}
}

func TestModelStepDownCrossesPageBoundary(t *testing.T) {
	m, _, _ := newTestModel()
	m.openProviderPanel()
	m.provPanel.step = provStepModel
	m.provPanel.chosenProvider = "openai"
	for i := 0; i < 22; i++ {
		m.provPanel.models = append(m.provPanel.models, fmt.Sprintf("model-%02d", i))
	}
	m.provPanel.modelIdx = 14

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.provPanel.modelIdx != 15 {
		t.Fatalf("expected modelIdx 15 after down across boundary, got %d", m.provPanel.modelIdx)
	}
	if !strings.Contains(m.renderProviderPanel(), "page 2/2") {
		t.Fatalf("expected render to show page 2/2 after crossing boundary")
	}
}

func TestModelStepUpCrossesPageBoundary(t *testing.T) {
	m, _, _ := newTestModel()
	m.openProviderPanel()
	m.provPanel.step = provStepModel
	m.provPanel.chosenProvider = "openai"
	for i := 0; i < 22; i++ {
		m.provPanel.models = append(m.provPanel.models, fmt.Sprintf("model-%02d", i))
	}
	m.provPanel.modelIdx = 15

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.provPanel.modelIdx != 14 {
		t.Fatalf("expected modelIdx 14 after up across boundary, got %d", m.provPanel.modelIdx)
	}
	if !strings.Contains(m.renderProviderPanel(), "page 1/2") {
		t.Fatalf("expected render to show page 1/2 after crossing boundary")
	}
}
