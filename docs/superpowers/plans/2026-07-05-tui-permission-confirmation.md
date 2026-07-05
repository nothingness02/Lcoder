# TUI Permission Confirmation Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the full-screen TUI permission modal with an inline bottom confirmation strip that keeps logs visible and uses left/right arrow selection + Enter confirmation.

**Architecture:** The existing `stateConfirm` state and blocking `tuiConfirm` goroutine are preserved. Only the rendering and key handling change: `confirmPanel` gains a `selected` index and renders a bottom strip; `View()` keeps the conversation viewport visible; `handleConfirmKey` routes arrow keys to option selection, Enter/Esc to decision, and scroll keys to the viewport.

**Tech Stack:** Go, Bubble Tea, Lipgloss, existing `pkg/tui` theme helpers.

---

## File Map

- `pkg/tui/confirm.go` — `confirmPanel` state, navigation, and bottom-strip rendering.
- `pkg/tui/view.go` — remove full-screen `confirmView`; render confirmation strip in `bottomRegion`.
- `pkg/tui/keys.go` — arrow/Enter/Esc/scroll handling in `handleConfirmKey`.
- `pkg/tui/model.go` — call `updateSizes()` on confirm show/hide so viewport height adjusts.
- `pkg/tui/confirm_test.go` — update/add tests for selection, rendering, and scrolling.

---

## Task 1: Extend `confirmPanel` with selection state and bottom-strip rendering

**Files:**
- Modify: `pkg/tui/confirm.go`
- Test: `pkg/tui/confirm_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/tui/confirm_test.go`:

```go
func TestConfirmPanelSelectionAndDecision(t *testing.T) {
	p := &confirmPanel{}
	p.show(agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "ls"}},
	}, make(chan confirmResult, 1))

	if !p.visible {
		t.Fatal("panel should be visible")
	}
	if p.selected != 0 {
		t.Fatalf("default selection should be Allow(0), got %d", p.selected)
	}

	p.next()
	if p.selected != 1 {
		t.Fatalf("next should move to Deny(1), got %d", p.selected)
	}
	p.prev()
	if p.selected != 0 {
		t.Fatalf("prev should move back to Allow(0), got %d", p.selected)
	}

	if !p.confirm() {
		t.Fatal("confirm on Allow should return true")
	}
	p.next()
	if p.confirm() {
		t.Fatal("confirm on Deny should return false")
	}
}
```

Run:

```bash
go test ./pkg/tui -run TestConfirmPanelSelectionAndDecision -v
```

Expected: FAIL — `selected`, `next`, `prev`, `confirm` undefined.

- [ ] **Step 2: Implement `confirmPanel` changes**

Replace the contents of `pkg/tui/confirm.go` (except imports and `formatArgs`) with:

```go
// confirmResult is returned to the blocked tool call goroutine.
type confirmResult struct {
	allow bool
	err   error
}

// confirmRequest carries a pending confirmation into the Bubble Tea loop.
type confirmRequest struct {
	info agent.ToolCallInfo
	resp chan confirmResult
}

// confirmRequestMsg asks the UI to show a permission prompt.
type confirmRequestMsg struct {
	req confirmRequest
}

// confirmResponseMsg carries the user's decision back into the loop.
type confirmResponseMsg struct {
	allow bool
}

// programSender matches the part of *tea.Program that tuiConfirm needs.
type programSender interface {
	Send(tea.Msg)
}

// tuiConfirm implements agent.UserConfirmation by delegating to the Bubble Tea
// event loop. It blocks the tool-call goroutine until the user responds.
type tuiConfirm struct {
	program programSender
}

func (c *tuiConfirm) Confirm(ctx context.Context, info agent.ToolCallInfo) (bool, error) {
	req := confirmRequest{info: info, resp: make(chan confirmResult)}
	c.program.Send(confirmRequestMsg{req: req})
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case r := <-req.resp:
		return r.allow, r.err
	}
}

// confirmPanel renders an interactive permission prompt as a bottom strip.
type confirmPanel struct {
	visible bool
	selected int // 0 = Allow, 1 = Deny
	info    agent.ToolCallInfo
	resp    chan confirmResult
}

func (p *confirmPanel) show(info agent.ToolCallInfo, resp chan confirmResult) {
	p.visible = true
	p.selected = 0
	p.info = info
	p.resp = resp
}

func (p *confirmPanel) hide() {
	p.visible = false
	p.selected = 0
	p.info = agent.ToolCallInfo{}
	p.resp = nil
}

func (p *confirmPanel) next() {
	if !p.visible {
		return
	}
	p.selected = (p.selected + 1) % 2
}

func (p *confirmPanel) prev() {
	if !p.visible {
		return
	}
	p.selected = (p.selected - 1 + 2) % 2
}

func (p *confirmPanel) confirm() bool {
	return p.selected == 0
}

func (p *confirmPanel) View(width int) string {
	if !p.visible {
		return ""
	}
	if width <= 0 {
		width = 80
	}

	prompt := fmt.Sprintf("Permission request: %s", p.info.ToolCall.Name)
	if args := formatArgs(p.info.Args); args != "" {
		prompt += " " + args
	}

	allowStyle := optionStyle(p.selected == 0)
	denyStyle := optionStyle(p.selected == 1)
	options := lipgloss.JoinHorizontal(lipgloss.Left,
		allowStyle.Render("Allow"),
		"  ",
		denyStyle.Render("Deny"),
	)

	hint := styleDim().Render("← → select · Enter confirm · Esc cancel")
	line := lipgloss.JoinHorizontal(lipgloss.Left, options, "    ", hint)

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(colorError).
		Padding(0, 1).
		Width(width)

	return box.Render(lipgloss.JoinVertical(lipgloss.Left, prompt, line))
}

func optionStyle(selected bool) lipgloss.Style {
	if selected {
		return lipgloss.NewStyle().
			Background(colorError).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Bold(true)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
}
```

`formatArgs` remains unchanged.

Run:

```bash
go test ./pkg/tui -run TestConfirmPanelSelectionAndDecision -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/tui/confirm.go pkg/tui/confirm_test.go
git commit -m "feat(tui): add selectable bottom-strip confirm panel

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Render the confirmation strip in `bottomRegion` instead of full-screen modal

**Files:**
- Modify: `pkg/tui/view.go`
- Modify: `pkg/tui/model.go` (trigger `updateSizes` on confirm transitions)

- [ ] **Step 1: Write the failing test**

Add to `pkg/tui/confirm_test.go`:

```go
func TestConfirmPanelRendersAsBottomStrip(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 24
	m.updateSizes()

	resp := make(chan confirmResult, 1)
	m2, _ := m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash", Arguments: map[string]any{"command": "ls"}}},
		resp: resp,
	}})
	mm := m2.(*Model)

	view := mm.View()
	if !strings.Contains(view, "Permission request: bash") {
		t.Fatalf("view missing permission prompt:\n%s", view)
	}
	if !strings.Contains(view, "Allow") {
		t.Fatalf("view missing Allow option:\n%s", view)
	}
	if !strings.Contains(view, "Deny") {
		t.Fatalf("view missing Deny option:\n%s", view)
	}
	if strings.Contains(view, "Type a message") {
		t.Fatalf("input box should be hidden while confirming:\n%s", view)
	}
}
```

Add `strings` import to `confirm_test.go` if missing.

Run:

```bash
go test ./pkg/tui -run TestConfirmPanelRendersAsBottomStrip -v
```

Expected: FAIL — view still returns full-screen modal, so input box is hidden and the strip layout is not present.

- [ ] **Step 2: Update `View()` and `bottomRegion()`**

In `pkg/tui/view.go`:

1. Remove the `stateConfirm` branch from `View()`:

```go
func (m Model) View() string {
	switch m.state {
	case stateStartup:
		return m.startupView()
	case stateSessionPicker:
		return m.picker.View()
	case stateExtensions:
		return m.extPanel.View(m.width, m.height)
	case stateProvider:
		return m.renderProviderPanel()
	}

	top := m.viewport.View()
	bottom := m.bottomRegion()
	main := lipgloss.JoinVertical(lipgloss.Left, top, bottom)
	if m.taskSidebarVisible() {
		return lipgloss.JoinHorizontal(lipgloss.Top, main, renderTaskSidebar(m.tasks, m.height))
	}
	return main
}
```

2. Remove the unused `confirmView()` function entirely.

3. Update `bottomRegion()` to render the confirmation strip when confirming:

```go
func (m *Model) bottomRegion() string {
	var sections []string

	if m.state == stateConfirm {
		sections = append(sections, m.confirm.View(m.mainWidth))
		return strings.Join(sections, "\n")
	}

	if m.menuVisible {
		matches := menuMatches(m.input.Value())
		sections = append(sections, renderMenu(matches, m.menuSelected, m.mainWidth))
	} else if m.fileMenuVisible {
		sections = append(sections, renderFileMenu(m.fileMenuItems, m.fileMenuSelected, m.mainWidth))
	} else if m.cmdPanel.visible {
		sections = append(sections, renderCmdPanel(m.cmdPanel, m.mainWidth))
	}

	sections = append(sections, m.input.View())

	if m.suggestion != "" {
		sections = append(sections, styleFaint().Render("  "+m.suggestion))
	}

	sections = append(sections, m.statusLineView())

	return strings.Join(sections, "\n")
}
```

- [ ] **Step 3: Adjust viewport height on confirm transitions**

In `pkg/tui/model.go`, update the `Update` cases for confirm messages:

```go
case confirmRequestMsg:
	m.confirm.show(msg.req.info, msg.req.resp)
	m.state = stateConfirm
	m.updateSizes()
	return m, nil

case confirmResponseMsg:
	if m.confirm.visible && m.confirm.resp != nil {
		m.confirm.resp <- confirmResult{allow: msg.allow}
	}
	m.confirm.hide()
	m.state = stateProcessing
	m.updateSizes()
	return m, nil
```

Run:

```bash
go test ./pkg/tui -run TestConfirmPanelRendersAsBottomStrip -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/tui/view.go pkg/tui/model.go pkg/tui/confirm_test.go
git commit -m "feat(tui): render confirmation strip inline above the log

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Update key handling for arrow selection and log scrolling

**Files:**
- Modify: `pkg/tui/keys.go`
- Test: `pkg/tui/confirm_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/tui/confirm_test.go`:

```go
func TestConfirmPanelArrowSelection(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	resp := make(chan confirmResult, 1)
	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: resp,
	}})

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	mm := m2.(*Model)
	if mm.confirm.selected != 1 {
		t.Fatalf("right arrow should select Deny, got %d", mm.confirm.selected)
	}

	m3, _ := mm.Update(tea.KeyMsg{Type: tea.KeyLeft})
	mm = m3.(*Model)
	if mm.confirm.selected != 0 {
		t.Fatalf("left arrow should select Allow, got %d", mm.confirm.selected)
	}
}

func TestConfirmPanelEnterDecision(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	resp := make(chan confirmResult, 1)
	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: resp,
	}})

	// Move to Deny and confirm.
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(*Model)
	if mm.state != stateProcessing {
		t.Fatalf("expected stateProcessing after Enter, got %v", mm.state)
	}
	if cmd == nil {
		t.Fatal("expected a command to send confirmResponseMsg")
	}

	// Drive the command to deliver the message.
	msg := cmd()
	cmsg, ok := msg.(confirmResponseMsg)
	if !ok {
		t.Fatalf("expected confirmResponseMsg, got %T", msg)
	}
	if cmsg.allow {
		t.Fatal("Deny selection should produce allow=false")
	}
}
```

Run:

```bash
go test ./pkg/tui -run 'TestConfirmPanelArrowSelection|TestConfirmPanelEnterDecision' -v
```

Expected: FAIL — `handleConfirmKey` does not handle arrows/Enter yet.

- [ ] **Step 2: Implement arrow/Enter/Esc/scroll handling**

Replace `handleConfirmKey` in `pkg/tui/keys.go` with:

```go
// handleConfirmKey handles selection, confirmation, and log scrolling while a
// permission prompt is active.
func (m *Model) handleConfirmKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyLeft:
		m.confirm.prev()
		return m, nil
	case tea.KeyRight:
		m.confirm.next()
		return m, nil
	case tea.KeyEnter:
		return m, func() tea.Msg { return confirmResponseMsg{allow: m.confirm.confirm()} }
	case tea.KeyEsc:
		return m, func() tea.Msg { return confirmResponseMsg{allow: false} }
	case tea.KeyRunes:
		switch strings.ToLower(string(k.Runes)) {
		case "y":
			return m, func() tea.Msg { return confirmResponseMsg{allow: true} }
		case "n":
			return m, func() tea.Msg { return confirmResponseMsg{allow: false} }
		}
	}

	// Let the viewport handle scroll keys (PgUp/PgDn/Up/Down/Home/End) so the
	// user can review logs while the prompt is waiting.
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(k)
	return m, cmd
}
```

Run:

```bash
go test ./pkg/tui -run 'TestConfirmPanelArrowSelection|TestConfirmPanelEnterDecision' -v
```

Expected: PASS.

- [ ] **Step 3: Add log-scrolling test**

Add to `pkg/tui/confirm_test.go`:

```go
func TestConfirmPanelCanScrollLog(t *testing.T) {
	m := NewModel(events.New(), &fakeAgent{}, &fakeSession{}, &fakeSessionStore{}, ".", "s1", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false)
	m.width = 80
	m.height = 10
	m.updateSizes()

	// Fill the viewport with enough content to scroll.
	for i := 0; i < 50; i++ {
		m.blocks = append(m.blocks, textBlock{content: fmt.Sprintf("line %d", i)})
	}
	m.rebuildViewport()

	m.Update(confirmRequestMsg{req: confirmRequest{
		info: agent.ToolCallInfo{ToolCall: models.ToolCallContent{Name: "bash"}},
		resp: make(chan confirmResult, 1),
	}})

	before := m.viewport.YOffset
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	mm := m2.(*Model)
	if mm.viewport.YOffset == before {
		t.Fatal("expected viewport to scroll up while confirming")
	}
}
```

Add `fmt` import if missing.

Run:

```bash
go test ./pkg/tui -run TestConfirmPanelCanScrollLog -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/tui/keys.go pkg/tui/confirm_test.go
git commit -m "feat(tui): arrow selection and log scrolling in permission prompt

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Final verification and full test run

- [ ] **Step 1: Run package tests**

```bash
go test ./pkg/tui -count=1
```

Expected: all PASS.

- [ ] **Step 2: Run vet**

```bash
go vet ./pkg/tui
```

Expected: no issues.

- [ ] **Step 3: Run full suite (excluding reference/Shannon)**

```bash
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
```

Expected: all PASS.

- [ ] **Step 4: Final commit if any fixes were needed**

If no fixes, no extra commit. If fixes, commit with:

```bash
git add -A
git commit -m "fix(tui): address review/test issues in permission confirmation

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Self-Review Checklist

- [x] Spec coverage: bottom-strip layout, arrow selection, Enter/Esc, scrollable logs, and tests are all mapped to tasks.
- [x] No placeholders: every step includes exact code, file paths, and commands.
- [x] Type consistency: `confirmPanel` uses `selected int` (0/1), `next()`/`prev()`/`confirm()` across all tasks.
