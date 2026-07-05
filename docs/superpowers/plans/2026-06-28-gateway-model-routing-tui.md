# Provider/Model TUI Wizard + Runtime Live Switch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a TUI wizard (first-launch + `/provider`) that selects provider -> model -> api_key, persists keys to `credentials.yaml`, hot-registers the provider with the gateway, and switches the live session's model + context budget without a restart.

**Architecture:** A new `providerPanel` overlay (plain struct routed by the parent `Model`, mirroring `cmdPanel`/`SessionPickerModel`) drives a 3-step state machine. The `Model` gains `*llm.Client` + `config.Config` so it can list models, recompute the budget, and hot-register providers. A new `Agent.SwitchModel(ref, budget)` mutates the live agent's model and context-manager budget in place (preserving conversation history). `main.go` threads the client/config in and detects a missing key on first launch.

**Tech Stack:** Go, Bubble Tea (`bubbletea`/`bubbles`), `bubbles/textinput` (already vendored in the `bubbles` module — no `go.mod` change), the existing LiteLLM gateway over HTTP.

---

## Background facts (verified against the code — do not re-derive)

- `config.ProviderInfo{Name, Display, KeyEnv, Route, DefaultBase string}`, `config.BuiltinProviders []ProviderInfo`, `config.BuiltinProvider(name) (ProviderInfo, bool)` — `pkg/config/builtin_providers.go`.
- `config.ProviderConn{BaseURL, APIKey, Route string; Headers map[string]string}` — `pkg/config/providers.go:12`.
- `config.LoadCredentials(path) (map[string]ProviderConn, error)`, `config.SaveCredentials(path, creds) error`, **unexported** `resolveCredentialsPath() string` — `pkg/config/credentials.go`.
- `config.Config` has `Provider string`, `Model string`, `Providers map[string]ProviderConn`, `Catalog ModelCatalog`, and `ResolveContextBudget(litellmWindow int) (TokenBudget, string)`. `config.TokenBudget` has `MaxTotal, TargetTotal, ReserveOutput int` and `CompactThreshold float64`.
- `config.ModelCatalog{Models []ModelMeta}`; `ModelMeta` has `ID string`, `Provider string`.
- `llm.NewClient(baseURL) *Client`; `(*Client).ListModels(ctx) ([]models.ModelInfo, error)`; `(*Client).RegisterProvider(ctx, name string, conn config.ProviderConn) error`.
- `models.ModelInfo{ID, Provider string; Aliases, Capabilities []string; ContextWindow int}`; `models.ModelRef{Provider, ID string}`.
- `contextmgr.TokenBudget{MaxTotal, TargetTotal, ReserveOutput int; CompactThreshold, DropThreshold float64}`; `contextmgr.Manager` holds an unexported `budget TokenBudget` field — `pkg/contextmgr/manager.go:53`.
- `agent.Agent` holds `cfg Config` (with `cfg.Model models.ModelRef`), `mgr *contextmgr.Manager`, `mu sync.Mutex` — `pkg/agent/loop.go:36`. The loop reads the model per-turn via `applyMode()` starting from `a.cfg.Model` (`loop.go:797`).
- TUI `Model` (`pkg/tui/model.go:26`) holds `agent AgentRunner`, `model string` (display only), `header headerInfo` (`header.model`), `state uiState`. It does **not** currently hold a client, config, or `ModelRef`.
- `AgentRunner` interface — `pkg/tui/messages.go:57`.
- `NewModel(bus, ag, session, store, cwd, sessionID, model, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, loadedSkills ...skills.Skill) *Model` — `pkg/tui/model.go:93`.
- `tui.Run(...)` and `tui.RunWithIO(...)` both call `NewModel` — `pkg/tui/app.go:16,37`.
- `uiState` enum + `handleKey` state switch — `pkg/tui/keys.go:69`. Slash registry `commandRegistry` — `pkg/tui/menu.go:20`. Slash dispatch `dispatchSlash` — `pkg/tui/keys.go:392`.
- `agentSetup` struct (`cmd/lcoder/main.go:121`) has `ag, sess, store, bus, mcpRegistry, cfg (agentConfig), cwd, cleanup` — **no client field yet**. `agentConfig` embeds `config.Config` (`main.go:132`). `llmClient` is created at `main.go:175` but not stored. `runTUI` builds the `tui.Run` call at `main.go:422-448`. `lookupModelWindow(client, provider, model) int` lives at `main.go:922`.
- Test helpers: `newTestModel()` (`pkg/tui/model_test.go:63`) builds a `Model` with a `fakeAgent`; `fakeAgent` implements `AgentRunner`. `modelsServer(t, status, body) *llm.Client` (`cmd/lcoder/lookup_test.go:13`) is the httptest pattern for a gateway client.

## Out of scope (YAGNI — deferred, with rationale)

- **Persisting the selected provider/model back to `config.yaml`.** The live switch (`SwitchModel`) applies the choice to the running session immediately, and the api key is durably saved to `credentials.yaml`. Safely rewriting a hand-authored `config.yaml` (preserving comments/other keys) needs a config-file path resolver + YAML writer that do not exist yet — its own sub-project. After restart, the user's `config.yaml` `provider:`/`model:` still govern startup; changing the default is a one-line manual edit. Build a `config.SaveSelection` in a follow-up if desired.
- Async (`tea.Cmd`) model-list fetching. The wizard fetches synchronously with a 5s timeout when entering the model step (mirrors `SessionPickerModel`'s synchronous store reads). A brief block on a config screen is acceptable.
- Provider health probing, multi-key rotation, base_url/headers editing in the UI beyond api_key (the built-in table supplies `DefaultBase`/`Route`).
- Rebuilding the summarizer's model on switch. `SwitchModel` updates `a.cfg.Model` + budget; the compaction summarizer keeps its original model ref (only affects which model summarizes — not correctness).

---

## Task 1: Export credentials path + provider-key probe (`pkg/config`)

**Files:**
- Modify: `pkg/config/credentials.go`
- Test: `pkg/config/credentials_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/config/credentials_test.go`:

```go
func TestCredentialsPathExported(t *testing.T) {
	// Exported accessor must return the same value as the internal resolver.
	if CredentialsPath() != resolveCredentialsPath() {
		t.Fatalf("CredentialsPath()=%q != resolveCredentialsPath()=%q", CredentialsPath(), resolveCredentialsPath())
	}
}

func TestProviderHasKey(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConn{
		"openai": {APIKey: "sk-config"},
	}}

	// Key present in merged config.providers.
	if !ProviderHasKey(cfg, "openai") {
		t.Fatal("expected openai to have a key from config.providers")
	}

	// No config key, no env -> false.
	t.Setenv("ANTHROPIC_API_KEY", "")
	if ProviderHasKey(cfg, "anthropic") {
		t.Fatal("expected anthropic to lack a key")
	}

	// No config key, but standard env var set -> true.
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	if !ProviderHasKey(cfg, "anthropic") {
		t.Fatal("expected anthropic to have a key via env var")
	}

	// Unknown provider with no config/env -> false (no panic).
	if ProviderHasKey(cfg, "mystery") {
		t.Fatal("expected unknown provider to lack a key")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/config/ -run 'CredentialsPathExported|ProviderHasKey' -v`
Expected: FAIL — `undefined: CredentialsPath`, `undefined: ProviderHasKey`.

- [ ] **Step 3: Write the implementation**

Append to `pkg/config/credentials.go` (the file already imports `os`):

```go
// CredentialsPath is the exported accessor for the TUI-managed credentials file
// (~/.lcoder/credentials.yaml). Returns "" when the home dir is unavailable.
func CredentialsPath() string {
	return resolveCredentialsPath()
}

// ProviderHasKey reports whether the given provider already has a usable api key,
// checking the merged config.providers map first (which Load() folds credentials
// into) and then the provider's standard environment variable from the built-in
// table. Used to decide whether the first-launch wizard should fire.
func ProviderHasKey(cfg Config, provider string) bool {
	if conn, ok := cfg.Providers[provider]; ok && conn.APIKey != "" {
		return true
	}
	if info, ok := BuiltinProvider(provider); ok && info.KeyEnv != "" {
		if os.Getenv(info.KeyEnv) != "" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/config/ -run 'CredentialsPathExported|ProviderHasKey' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/config/credentials.go pkg/config/credentials_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(config): export CredentialsPath and ProviderHasKey probe"
```

---

## Task 2: Gateway model-window lookup on the client (`pkg/llm`)

**Files:**
- Modify: `pkg/llm/client.go`
- Test: `pkg/llm/window_test.go` (create)

This moves the exact+prefix match logic (currently inlined in `cmd/lcoder/main.go:922`) onto the client so both `main.go` and the TUI can reuse it. Task 10 refactors `main.go` to delegate here.

- [ ] **Step 1: Write the failing test**

Create `pkg/llm/window_test.go`:

```go
package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func windowServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func TestModelWindowExactMatch(t *testing.T) {
	c := windowServer(t, `[{"id":"gpt-4o","provider":"openai","context_window":128000}]`)
	w, err := c.ModelWindow(context.Background(), "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 128000 {
		t.Fatalf("expected 128000, got %d", w)
	}
}

func TestModelWindowPrefixMatch(t *testing.T) {
	c := windowServer(t, `[{"id":"claude-sonnet-4","provider":"anthropic","context_window":200000}]`)
	w, _ := c.ModelWindow(context.Background(), "anthropic", "claude-sonnet-4-20250514")
	if w != 200000 {
		t.Fatalf("expected 200000 via prefix, got %d", w)
	}
}

func TestModelWindowProviderMismatch(t *testing.T) {
	c := windowServer(t, `[{"id":"gpt-4o","provider":"openai","context_window":128000}]`)
	w, _ := c.ModelWindow(context.Background(), "azure", "gpt-4o")
	if w != 0 {
		t.Fatalf("expected 0 for provider mismatch, got %d", w)
	}
}

func TestModelWindowRequestFailure(t *testing.T) {
	c := NewClient("http://127.0.0.1:1") // nothing listening
	w, err := c.ModelWindow(context.Background(), "openai", "gpt-4o")
	if err == nil {
		t.Fatal("expected error when gateway unreachable")
	}
	if w != 0 {
		t.Fatalf("expected 0 on failure, got %d", w)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/llm/ -run ModelWindow -v`
Expected: FAIL — `c.ModelWindow undefined`.

- [ ] **Step 3: Write the implementation**

Append to `pkg/llm/client.go` (the file already imports `context` and `strings`):

```go
// ModelWindow returns the context window the gateway auto-discovered for the
// given provider/model, or 0 if unknown. It prefers an exact provider+id match,
// then a prefix match so dated variants resolve to a base-id entry. A transport
// failure is returned as an error (with window 0) so callers can distinguish
// "unreachable" from "known to be unknown".
func (c *Client) ModelWindow(ctx context.Context, provider, model string) (int, error) {
	list, err := c.ListModels(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range list {
		if m.Provider == provider && m.ID == model && m.ContextWindow > 0 {
			return m.ContextWindow, nil
		}
	}
	for _, m := range list {
		if m.Provider != provider || m.ContextWindow <= 0 {
			continue
		}
		if strings.HasPrefix(m.ID, model) || strings.HasPrefix(model, m.ID) {
			return m.ContextWindow, nil
		}
	}
	return 0, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/llm/ -run ModelWindow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/llm/client.go pkg/llm/window_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(llm): add Client.ModelWindow gateway window lookup"
```

---

## Task 3: In-place budget setter on the context manager (`pkg/contextmgr`)

**Files:**
- Modify: `pkg/contextmgr/manager.go`
- Test: `pkg/contextmgr/manager_budget_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `pkg/contextmgr/manager_budget_test.go`:

```go
package contextmgr

import "testing"

func TestSetBudgetReplacesInPlace(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 1000, TargetTotal: 900, ReserveOutput: 100})
	if got := m.Budget().MaxTotal; got != 1000 {
		t.Fatalf("initial MaxTotal = %d, want 1000", got)
	}

	m.SetBudget(TokenBudget{MaxTotal: 200000, TargetTotal: 180000, ReserveOutput: 8192, CompactThreshold: 0.9})

	b := m.Budget()
	if b.MaxTotal != 200000 || b.TargetTotal != 180000 || b.ReserveOutput != 8192 {
		t.Fatalf("SetBudget did not replace budget, got %+v", b)
	}
	if b.CompactLimit() != int(180000*0.9) {
		t.Fatalf("CompactLimit = %d, want %d", b.CompactLimit(), int(180000*0.9))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/contextmgr/ -run SetBudgetReplacesInPlace -v`
Expected: FAIL — `m.SetBudget undefined`, `m.Budget undefined`.

- [ ] **Step 3: Write the implementation**

Add to `pkg/contextmgr/manager.go` (after `NewManager`, around line 91):

```go
// SetBudget replaces the manager's token budget in place. Blocks and history are
// untouched, so a live model switch can re-size the budget without losing the
// conversation.
func (m *Manager) SetBudget(b TokenBudget) {
	m.budget = b
}

// Budget returns the manager's current token budget.
func (m *Manager) Budget() TokenBudget {
	return m.budget
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/contextmgr/ -run SetBudgetReplacesInPlace -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/contextmgr/manager.go pkg/contextmgr/manager_budget_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(contextmgr): add Manager.SetBudget/Budget for live re-sizing"
```

---

## Task 4: Live model switch on the agent (`pkg/agent`)

**Files:**
- Modify: `pkg/agent/loop.go`
- Test: `pkg/agent/switch_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `pkg/agent/switch_test.go` (white-box, package `agent`):

```go
package agent

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestSwitchModelUpdatesModelAndBudget(t *testing.T) {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{MaxTotal: 1000, TargetTotal: 900, ReserveOutput: 100})
	a := &Agent{
		cfg: Config{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"}},
		mgr: mgr,
	}

	a.SwitchModel(
		models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"},
		contextmgr.TokenBudget{MaxTotal: 200000, TargetTotal: 180000, ReserveOutput: 8192, CompactThreshold: 0.9},
	)

	if a.cfg.Model.Provider != "anthropic" || a.cfg.Model.ID != "claude-sonnet-4" {
		t.Fatalf("model not switched, got %+v", a.cfg.Model)
	}
	if a.mgr.Budget().MaxTotal != 200000 {
		t.Fatalf("budget not updated, got %d", a.mgr.Budget().MaxTotal)
	}
}

func TestSwitchModelNilManager(t *testing.T) {
	a := &Agent{cfg: Config{Model: models.ModelRef{Provider: "openai", ID: "x"}}}
	// Must not panic when the manager is nil (TransformContext path).
	a.SwitchModel(models.ModelRef{Provider: "deepseek", ID: "deepseek-chat"}, contextmgr.TokenBudget{MaxTotal: 64000})
	if a.cfg.Model.Provider != "deepseek" {
		t.Fatalf("model not switched, got %+v", a.cfg.Model)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/agent/ -run SwitchModel -v`
Expected: FAIL — `a.SwitchModel undefined`.

- [ ] **Step 3: Write the implementation**

Add to `pkg/agent/loop.go` (the file already imports `contextmgr` and `models`). Place after the `Agent` struct / near other `*Agent` methods:

```go
// SwitchModel changes the model used for subsequent turns and re-sizes the
// context budget in place. Conversation history is preserved. Intended to be
// called from the TUI while the agent is idle (the provider overlay is modal).
func (a *Agent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.Model = ref
	if a.mgr != nil {
		a.mgr.SetBudget(budget)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/agent/ -run SwitchModel -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/agent/loop.go pkg/agent/switch_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(agent): add Agent.SwitchModel for runtime model/budget switch"
```

---

## Task 5: TUI plumbing — interface, Model fields, constructor signatures

**Files:**
- Modify: `pkg/tui/messages.go` (AgentRunner interface)
- Modify: `pkg/tui/model.go` (Model fields + NewModel)
- Modify: `pkg/tui/app.go` (Run + RunWithIO)
- Modify: `pkg/tui/model_test.go` (fakeAgent + newTestModel)

This is wiring only; the panel itself comes in Tasks 6-9.

- [ ] **Step 1: Extend the AgentRunner interface and fakeAgent**

In `pkg/tui/messages.go`, add the method to the `AgentRunner` interface (after `Abort()`):

```go
	Abort()                        // esc-to-interrupt
	SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget)
```

Add the `contextmgr` import to `pkg/tui/messages.go` if not present:

```go
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/models"
```

In `pkg/tui/model_test.go`, add to `fakeAgent` a recorded implementation (and import `contextmgr`):

```go
func (f *fakeAgent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	f.switchedModel = ref
	f.switchedBudget = budget
}
```

And add the fields to the `fakeAgent` struct:

```go
	switchedModel  models.ModelRef
	switchedBudget contextmgr.TokenBudget
```

- [ ] **Step 2: Add Model fields and extend NewModel**

In `pkg/tui/model.go`, add to the `Model` struct (near the `model string` display field):

```go
	// Provider-config wizard dependencies and state.
	llmClient          *llm.Client
	cfg                config.Config
	provPanel          providerPanel
	needsProviderSetup bool
```

Add imports to `pkg/tui/model.go`:

```go
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm"
```

Change the `NewModel` signature to accept the client, config, and first-launch flag (append them after `modeManager`, before the variadic `loadedSkills`):

```go
func NewModel(bus *events.Bus, ag AgentRunner, session SessionWriter, store SessionStore, cwd, sessionID, model, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, llmClient *llm.Client, cfg config.Config, needsProviderSetup bool, loadedSkills ...skills.Skill) *Model {
```

In the `&Model{...}` literal, set the new fields:

```go
		modeManager:        modeManager,
		llmClient:          llmClient,
		cfg:                cfg,
		needsProviderSetup: needsProviderSetup,
```

- [ ] **Step 3: Update Run and RunWithIO**

In `pkg/tui/app.go`, add imports `config` and `llm`:

```go
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm"
```

Change `Run` to accept and forward the new args:

```go
func Run(bus *events.Bus, ag *agent.Agent, sess *session.Session, store *session.Store, cwd, modelRef, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, capabilities []string, llmClient *llm.Client, cfg config.Config, needsProviderSetup bool, loadedSkills ...skills.Skill) error {
	model := NewModel(bus, ag, sess, store, cwd, sess.ID, modelRef, themeStyle, httpTools, mcpRegistry, modeManager, llmClient, cfg, needsProviderSetup, loadedSkills...)
	model.SetCapabilities(capabilities)
	defer model.Close()
```

Change `RunWithIO` similarly (it is used by tests/IO harness — keep it compiling):

```go
func RunWithIO(bus *events.Bus, ag *agent.Agent, sess *session.Session, store *session.Store, cwd, modelRef, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, llmClient *llm.Client, cfg config.Config, input *os.File, output *os.File, loadedSkills ...skills.Skill) (tea.Model, error) {
	model := NewModel(bus, ag, sess, store, cwd, sess.ID, modelRef, themeStyle, httpTools, mcpRegistry, modeManager, llmClient, cfg, false, loadedSkills...)
	defer model.Close()
```

- [ ] **Step 4: Update newTestModel helper**

In `pkg/tui/model_test.go`, update `newTestModel` to pass the new args (nil client, zero config, false flag):

```go
func newTestModel() (*Model, *fakeAgent, *fakeSession) {
	bus := events.New()
	agent := &fakeAgent{}
	sess := &fakeSession{id: "abc123"}
	store := &fakeSessionStore{}
	m := NewModel(bus, agent, sess, store, ".", "abc123", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, false)
	m.width = 80
	m.height = 24
	return m, agent, sess
}
```

Add the `config` import to `pkg/tui/model_test.go`:

```go
	"github.com/lcoder/lcoder/pkg/config"
```

NOTE: `providerPanel` is referenced by the new `Model.provPanel` field but is not defined until Task 6. To keep this task self-contained and compiling, add a **minimal placeholder type** at the top of a new file `pkg/tui/providerpanel.go` now:

```go
package tui

// providerPanel is fleshed out in Task 6. Declared here so Model compiles.
type providerPanel struct {
	visible bool
}
```

- [ ] **Step 5: Build and run the TUI + agent tests**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go build ./pkg/... && go test ./pkg/tui/ ./pkg/agent/ -count=1`
Expected: PASS (existing tui tests still green with the new constructor args). `cmd/lcoder` will NOT build yet (its `tui.Run` call is updated in Task 10) — that is expected; do not build `./cmd/...` in this task.

- [ ] **Step 6: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/tui/messages.go pkg/tui/model.go pkg/tui/app.go pkg/tui/model_test.go pkg/tui/providerpanel.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(tui): thread llm.Client/config into Model and AgentRunner.SwitchModel"
```

---

## Task 6: Provider panel — struct + provider step + open/close + render

**Files:**
- Modify: `pkg/tui/providerpanel.go` (replace the placeholder)
- Modify: `pkg/tui/keys.go` (new uiState + key routing)
- Test: `pkg/tui/providerpanel_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `pkg/tui/providerpanel_test.go`:

```go
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run 'ProviderPanel|ProviderStep' -v`
Expected: FAIL — `stateProvider`, `provStepProvider`, `openProviderPanel`, `BuiltinProvidersForPanel`, panel fields undefined.

- [ ] **Step 3: Replace `pkg/tui/providerpanel.go` with the real panel**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/lcoder/lcoder/pkg/config"
)

type provStep int

const (
	provStepProvider provStep = iota
	provStepModel
	provStepKey
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

	// manualModel captures a typed model id when the gateway returns no models.
	manualModel textinput.Model
	manual      bool

	keyInput textinput.Model

	chosenProvider string
	chosenModel    string
	errMsg         string
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
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%s\n", cursor, pi.Display))
		}
	case provStepModel:
		b.WriteString(fmt.Sprintf("Select a model for %s  (up/down, enter, esc=back)\n\n", p.chosenProvider))
		if p.manual {
			b.WriteString("No models discovered. Type a model id:\n\n")
			b.WriteString(p.manualModel.View())
		} else {
			for i, id := range p.models {
				cursor := "  "
				if i == p.modelIdx {
					cursor = "> "
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
	return b.String()
}
```

- [ ] **Step 4: Add the uiState and key routing**

In `pkg/tui/keys.go`, add `stateProvider` to the `uiState` const block in `pkg/tui/model.go` (where `stateExtensions` is defined):

```go
	stateExtensions
	stateProvider
)
```

In `pkg/tui/keys.go` `handleKey`, add a case before `default`:

```go
	case stateProvider:
		return m.handleProviderKey(k)
```

Add the provider-step handler to `pkg/tui/keys.go`:

```go
func (m *Model) handleProviderKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	switch m.provPanel.step {
	case provStepProvider:
		switch k.Type {
		case tea.KeyEsc:
			m.closeProviderPanel()
			return m, nil
		case tea.KeyUp:
			if m.provPanel.provIdx > 0 {
				m.provPanel.provIdx--
			}
			return m, nil
		case tea.KeyDown:
			if m.provPanel.provIdx < len(m.provPanel.providers)-1 {
				m.provPanel.provIdx++
			}
			return m, nil
		case tea.KeyEnter:
			return m, m.enterModelStep()
		}
		return m, nil
	}
	// provStepModel / provStepKey handled in Tasks 7 and 8.
	return m, nil
}
```

Add a temporary stub for `enterModelStep` at the bottom of `pkg/tui/providerpanel.go` (Task 7 replaces it):

```go
// enterModelStep is implemented in Task 7. Stub keeps Task 6 compiling/testable.
func (m *Model) enterModelStep() tea.Cmd {
	m.provPanel.chosenProvider = m.provPanel.providers[m.provPanel.provIdx].Name
	m.provPanel.step = provStepModel
	return nil
}
```

Add the `tea` import to `pkg/tui/providerpanel.go`:

```go
	tea "github.com/charmbracelet/bubbletea"
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run 'ProviderPanel|ProviderStep' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/tui/providerpanel.go pkg/tui/keys.go pkg/tui/model.go pkg/tui/providerpanel_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(tui): provider wizard panel with provider-selection step"
```

---

## Task 7: Provider panel — model step (fetch + list + manual fallback)

**Files:**
- Modify: `pkg/tui/providerpanel.go`
- Modify: `pkg/tui/keys.go`
- Test: `pkg/tui/providerpanel_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/tui/providerpanel_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"

	"github.com/lcoder/lcoder/pkg/llm"
)

// providerModelsServer serves a /v1/models list for the wizard fetch.
func providerModelsServer(t *testing.T, body string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return llm.NewClient(srv.URL)
}

func TestModelStepFetchFiltersByProvider(t *testing.T) {
	m, _, _ := newTestModel()
	m.llmClient = providerModelsServer(t,
		`[{"id":"gpt-4o","provider":"openai","context_window":128000},
		  {"id":"claude-sonnet-4","provider":"anthropic","context_window":200000}]`)
	m.openProviderPanel()
	// Provider 0 is openai per BuiltinProviders order.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.provPanel.step != provStepModel {
		t.Fatalf("expected provStepModel, got %v", m.provPanel.step)
	}
	if len(m.provPanel.models) != 1 || m.provPanel.models[0] != "gpt-4o" {
		t.Fatalf("expected [gpt-4o], got %v", m.provPanel.models)
	}
}

func TestModelStepManualFallbackWhenEmpty(t *testing.T) {
	m, _, _ := newTestModel()
	m.llmClient = providerModelsServer(t, `[]`) // gateway reachable but no models
	m.cfg = config.Config{}                     // no catalog fallback either
	m.openProviderPanel()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if !m.provPanel.manual {
		t.Fatal("expected manual model entry when no models discovered")
	}
}

func TestModelStepEnterAdvancesToKey(t *testing.T) {
	m, _, _ := newTestModel()
	m.llmClient = providerModelsServer(t, `[{"id":"gpt-4o","provider":"openai","context_window":128000}]`)
	m.openProviderPanel()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // provider -> model
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model -> key

	if m.provPanel.step != provStepKey {
		t.Fatalf("expected provStepKey, got %v", m.provPanel.step)
	}
	if m.provPanel.chosenModel != "gpt-4o" {
		t.Fatalf("expected chosenModel gpt-4o, got %q", m.provPanel.chosenModel)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run 'ModelStep' -v`
Expected: FAIL — the stub `enterModelStep` does not fetch; model-step keys not handled.

- [ ] **Step 3: Replace the `enterModelStep` stub and add model-step key handling**

In `pkg/tui/providerpanel.go`, replace the stub `enterModelStep` with the real fetch (synchronous, 5s timeout, with catalog fallback then manual):

```go
import block additions:
	"context"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
```

```go
// enterModelStep records the chosen provider and loads its model candidates from
// the gateway (/v1/models), falling back to the local catalog, then to manual
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

// fetchProviderModels returns model ids for the provider: gateway discovery first,
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

// avoid "imported and not used" if models ends up unreferenced in this file:
var _ = models.ModelRef{}
```

NOTE: remove the `var _ = models.ModelRef{}` line if `models` is referenced elsewhere in the file after Task 8 (Task 8's `commitProvider` uses `models.ModelRef`). It is a temporary guard for this task only.

In `pkg/tui/keys.go` `handleProviderKey`, add the `provStepModel` case (inside the `switch m.provPanel.step`):

```go
	case provStepModel:
		if m.provPanel.manual {
			switch k.Type {
			case tea.KeyEsc:
				m.provPanel.step = provStepProvider
				m.provPanel.manual = false
				return m, nil
			case tea.KeyEnter:
				m.enterKeyStep()
				return m, nil
			}
			var cmd tea.Cmd
			m.provPanel.manualModel, cmd = m.provPanel.manualModel.Update(k)
			return m, cmd
		}
		switch k.Type {
		case tea.KeyEsc:
			m.provPanel.step = provStepProvider
			return m, nil
		case tea.KeyUp:
			if m.provPanel.modelIdx > 0 {
				m.provPanel.modelIdx--
			}
			return m, nil
		case tea.KeyDown:
			if m.provPanel.modelIdx < len(m.provPanel.models)-1 {
				m.provPanel.modelIdx++
			}
			return m, nil
		case tea.KeyEnter:
			m.enterKeyStep()
			return m, nil
		}
		return m, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run 'ModelStep|ProviderStep|ProviderPanel' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/tui/providerpanel.go pkg/tui/keys.go pkg/tui/providerpanel_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(tui): model-selection step with gateway fetch and manual fallback"
```

---

## Task 8: Provider panel — key step + commit (save, register, switch)

**Files:**
- Modify: `pkg/tui/providerpanel.go`
- Modify: `pkg/tui/keys.go`
- Test: `pkg/tui/providerpanel_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/tui/providerpanel_test.go` (a server that serves `/v1/models` AND accepts `POST /v1/providers`):

```go
func TestCommitProviderSavesRegistersAndSwitches(t *testing.T) {
	var registered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"gpt-4o","provider":"openai","context_window":128000}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/providers":
			registered = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	m, agent, _ := newTestModel()
	m.llmClient = llm.NewClient(srv.URL)
	// Persist credentials to a temp HOME so we do not touch the real file.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	m.openProviderPanel()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // provider -> model
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model -> key
	// Type a key and submit.
	for _, r := range "sk-test" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit

	if m.state != stateInput || m.provPanel.visible {
		t.Fatalf("expected panel closed after commit, state=%v visible=%v", m.state, m.provPanel.visible)
	}
	if agent.switchedModel.ID != "gpt-4o" || agent.switchedModel.Provider != "openai" {
		t.Fatalf("expected agent switched to openai/gpt-4o, got %+v", agent.switchedModel)
	}
	if agent.switchedBudget.MaxTotal != 128000 {
		t.Fatalf("expected budget MaxTotal 128000 from gateway window, got %d", agent.switchedBudget.MaxTotal)
	}
	if !registered {
		t.Fatal("expected RegisterProvider POST to gateway")
	}
	if m.model != "openai/gpt-4o" {
		t.Fatalf("expected display model openai/gpt-4o, got %q", m.model)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run CommitProvider -v`
Expected: FAIL — key-step keys not handled / `commitProvider` undefined.

- [ ] **Step 3: Implement the key step + commit**

In `pkg/tui/providerpanel.go`, add imports and the commit method (the file already imports `context`, `time`, `strings`, `models`, `config`):

```go
	"github.com/lcoder/lcoder/pkg/contextmgr"
```

```go
// commitProvider persists the entered key, hot-registers the provider with the
// gateway, recomputes the context budget for the chosen model, switches the live
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
				m.errMsg = "网关热更新失败(下次启动生效): " + err.Error()
			}
		}
	}

	// Recompute the budget for the new model and switch the live agent.
	m.cfg.Provider = provName
	m.cfg.Model = modelID
	window := 0
	if m.llmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		window, _ = m.llmClient.ModelWindow(ctx, provName, modelID)
	}
	budget, _ := m.cfg.ResolveContextBudget(window)
	m.agent.SwitchModel(
		models.ModelRef{Provider: provName, ID: modelID},
		contextmgr.TokenBudget{
			MaxTotal:         budget.MaxTotal,
			TargetTotal:      budget.TargetTotal,
			ReserveOutput:    budget.ReserveOutput,
			CompactThreshold: budget.CompactThreshold,
		},
	)

	m.model = provName + "/" + modelID
	m.header.model = m.model
	m.closeProviderPanel()
}
```

Remove the temporary `var _ = models.ModelRef{}` guard added in Task 7 (now that `commitProvider` references `models.ModelRef`).

In `pkg/tui/keys.go` `handleProviderKey`, add the `provStepKey` case:

```go
	case provStepKey:
		switch k.Type {
		case tea.KeyEsc:
			m.provPanel.step = provStepModel
			m.provPanel.keyInput.Blur()
			return m, nil
		case tea.KeyEnter:
			m.commitProvider()
			return m, nil
		}
		var cmd tea.Cmd
		m.provPanel.keyInput, cmd = m.provPanel.keyInput.Update(k)
		return m, cmd
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run 'CommitProvider|ModelStep|ProviderStep|ProviderPanel' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/tui/providerpanel.go pkg/tui/keys.go pkg/tui/providerpanel_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(tui): api-key step + commit (save, register, live switch)"
```

---

## Task 9: `/provider` command + first-launch auto-open + render wiring

**Files:**
- Modify: `pkg/tui/menu.go` (registry)
- Modify: `pkg/tui/keys.go` (dispatch)
- Modify: `pkg/tui/model.go` (auto-open on startup + render the overlay in View)
- Test: `pkg/tui/providerpanel_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/tui/providerpanel_test.go`:

```go
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
	m := NewModel(bus, &fakeAgent{}, &fakeSession{id: "x"}, store, ".", "x",
		"openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, true /* needsProviderSetup */)
	defer m.Close()

	if m.state != stateProvider || !m.provPanel.visible {
		t.Fatalf("expected wizard auto-open on first launch, state=%v visible=%v", m.state, m.provPanel.visible)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -run 'SlashProvider|FirstLaunch' -v`
Expected: FAIL — `/provider` is unknown; no auto-open.

- [ ] **Step 3: Register the command and dispatch**

In `pkg/tui/menu.go`, add to `commandRegistry`:

```go
	{Name: "provider", Aliases: []string{"model"}, Description: "Configure LLM provider / model", Category: "System"},
```

In `pkg/tui/keys.go` `dispatchSlash`, add a case:

```go
	case "provider":
		m.openProviderPanel()
```

- [ ] **Step 4: Auto-open on first launch + render the overlay**

In `pkg/tui/model.go` `NewModel`, after the `&Model{...}` literal is assigned to `m` and before `return m`, add:

```go
	if needsProviderSetup {
		m.openProviderPanel()
	}
```

In `pkg/tui/model.go` `View()` (find the method; it renders based on `m.state`), add a branch so the provider overlay is shown. Locate where other overlays/states are rendered and add:

```go
	if m.state == stateProvider {
		return m.renderProviderPanel()
	}
```

(Place this near the existing overlay rendering, e.g. alongside the session picker / extensions panel handling, matching the surrounding return style.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/tui/ -count=1`
Expected: PASS (all tui tests, including the new ones).

- [ ] **Step 6: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add pkg/tui/menu.go pkg/tui/keys.go pkg/tui/model.go pkg/tui/providerpanel_test.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(tui): /provider command and first-launch wizard auto-open"
```

---

## Task 10: Wire `cmd/lcoder/main.go` (store client, thread args, detect first launch, DRY window lookup)

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: Store the gateway client in agentSetup**

In `cmd/lcoder/main.go`, add a field to `agentSetup` (struct at line 121):

```go
	llmClient   *llm.Client
```

In `prepareAgent`, where `llmClient := llm.NewClient(gatewayURL)` is created (line 175), and where the `&agentSetup{...}` is returned, set the field (find the return literal and add):

```go
		llmClient:   llmClient,
```

- [ ] **Step 2: Thread the new args through runTUI**

In `cmd/lcoder/main.go` `runTUI` (line 422), update the `tui.Run` call (line 448) to pass the client, the embedded `config.Config`, and the first-launch flag:

```go
	needsSetup := !config.ProviderHasKey(setup.cfg.Config, setup.cfg.Provider)
	return tui.Run(setup.bus, setup.ag, setup.sess, setup.store, setup.cwd, modelRef, setup.cfg.TUI.Theme, httpTools, setup.mcpRegistry, setup.cfg.modeManager, caps, setup.llmClient, setup.cfg.Config, needsSetup, setup.cfg.loadedSkills...)
```

(`setup.cfg` is an `agentConfig` that embeds `config.Config`; `setup.cfg.Config` is the embedded value, and `setup.cfg.Provider` resolves through the embed.)

- [ ] **Step 3: DRY the window lookup**

Replace the body of `lookupModelWindow` (line 922) to delegate to the new client method (keeps the existing call site at line 259 working):

```go
func lookupModelWindow(client *llm.Client, provider, model string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := client.ModelWindow(ctx, provider, model)
	if err != nil {
		return 0
	}
	return w
}
```

- [ ] **Step 4: Build the whole program + run all tests**

Run:
```bash
cd /d/code_practise/project/lab_pj/Lcoder && go build ./pkg/... ./cmd/... && go test ./pkg/... ./cmd/... -count=1
```
Expected: build succeeds; all tests PASS (including `cmd/lcoder` lookup tests, now exercising the delegated path).

- [ ] **Step 5: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add cmd/lcoder/main.go
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "feat(cmd): wire provider wizard client/config and first-launch detection"
```

---

## Task 11: Docs — document `/provider` and first-launch wizard

**Files:**
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: Add a doc comment**

In `configs/lcoder.yaml`, near the existing provider/api-key priority comment block (added in the backend plan, just above `context:`), append:

```yaml
# TUI 配置:首次启动若当前 provider 无可用 key,会自动弹出向导
# (选 provider -> 选 model -> 输入 api_key);运行中用 /provider 重新配置或切换模型。
# 向导写入的 api_key 保存到 ~/.lcoder/credentials.yaml(0600);切换的 model 立即对
# 当前会话生效(重启后仍以本文件的 provider/model 为默认)。
```

- [ ] **Step 2: Verify the file still parses (smoke)**

Run: `cd /d/code_practise/project/lab_pj/Lcoder && go test ./pkg/config/ -run Catalog -count=1`
Expected: PASS (config still loads; comment is inert).

- [ ] **Step 3: Commit**

```bash
git -C "D:/code_practise/project/lab_pj/Lcoder" add configs/lcoder.yaml
git -C "D:/code_practise/project/lab_pj/Lcoder" commit -m "docs(config): document /provider wizard and first-launch flow"
```

---

## Final verification (after all tasks)

```bash
cd /d/code_practise/project/lab_pj/Lcoder
go build ./pkg/... ./cmd/...
go vet ./pkg/... ./cmd/...
go test ./pkg/... ./cmd/... -count=1
cd gateway && .venv/Scripts/python -m pytest tests/ -q
```
All green. (Use `./pkg/... ./cmd/...`, NOT `./...` — the gitignored `reference/` tree pollutes the `./...` wildcard.)

Then dispatch the final whole-implementation code review and use superpowers:finishing-a-development-branch.

---

## Self-Review

**Spec coverage (against `2026-06-28-gateway-model-routing-tui-config-design.md` §8/§9):**
- §8 first-launch detection -> Task 1 (`ProviderHasKey`) + Task 9/10 (auto-open).
- §8 wizard 3 steps (provider -> model -> key) -> Tasks 6, 7, 8.
- §8 model list from `/v1/models` with catalog fallback -> Task 7.
- §8 write `credentials.yaml` (0600) -> Task 8 (`SaveCredentials`).
- §8 RegisterProvider when gateway online -> Task 8.
- §8/§9 runtime `/provider` switch with budget recompute -> Tasks 3, 4, 8 (`SwitchModel` + `ResolveContextBudget`).
- §9 `LCODER_PROVIDERS` already carries all providers (backend plan); the new key is also hot-registered live -> Task 8.
- §8 config.yaml provider/model write-back -> **explicitly deferred** (see Out of scope); the key persists and the live session switches.

**Placeholder scan:** Task 5 and Task 6/7 use clearly-labelled temporary stubs (`providerPanel` placeholder, `enterModelStep` stub, `var _ = models.ModelRef{}` guard) that later tasks replace; each is real compiling code, not a TODO. No "handle errors appropriately" hand-waves.

**Type consistency:** `SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget)` is identical in the interface (Task 5), the agent (Task 4), and the call site (Task 8). `contextmgr.TokenBudget` fields (`MaxTotal/TargetTotal/ReserveOutput/CompactThreshold`) match both `config.TokenBudget` (Task 8 conversion) and `manager.go`. `config.CredentialsPath`, `config.ProviderHasKey`, `Client.ModelWindow`, `Manager.SetBudget/Budget` names are used identically where defined and consumed.
