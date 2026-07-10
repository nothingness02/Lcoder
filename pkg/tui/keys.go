package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/skills"
)

// headerTickMsg drives the startup logo / header animation.
type headerTickMsg struct{}

// mcpActionMsg carries the result of an async MCP close/reconnect operation.
type mcpActionMsg struct {
	name string
	op   string
	err  error
}

func headerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return headerTickMsg{} })
}

// Update implements tea.Model. It returns tea.Model (not *Model) so *Model
// satisfies the bubbletea interface; the per-state handlers return *Model and
// are assignable to the tea.Model result.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateSizes()
		return m, nil

	case headerTickMsg:
		m.headerFrame++
		if m.state == stateStartup {
			return m, headerTick()
		}
		return m, nil

	case spinnerTickMsg:
		if m.state == stateProcessing {
			m.spinner.advance()
			return m, spinnerTick()
		}
		return m, nil

	case EventMsg:
		m.handleEvent(msg.Event)
		return m, waitForEventCmd(m.eventCh)

	case AgentDoneMsg:
		m.onAgentDone(msg.Err)
		return m, nil

	case mcpActionMsg:
		if msg.err != nil {
			m.showTextPanel("mcp", styleError().Render(fmt.Sprintf("%s %s failed: %v", msg.op, msg.name, msg.err)))
		} else {
			m.showTextPanel("mcp", styleSuccess().Render(fmt.Sprintf("%s %s succeeded", msg.op, msg.name)))
		}
		m.extPanel.SetMCPServers(mcpServers(m.mcpRegistry))
		return m, nil

	case SendPromptMsg:
		return m, m.startPrompt(msg.Text)

	case confirmRequestMsg:
		m.confirm.show(msg.req.info, msg.req.resp)
		m.state = stateConfirm
		m.updateSizes()
		return m, nil

	case confirmResponseMsg:
		if m.confirm.visible && m.confirm.resp != nil {
			res := m.confirm.confirm()
			m.confirm.resp <- confirmResult{allow: res.Allow, scope: res.Scope}
		}
		m.confirm.hide()
		m.state = stateProcessing
		m.updateSizes()
		return m, nil

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m.forwardMsg(msg)
}

// handleKey routes a key by the current state.
func (m *Model) handleKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch m.state {
	case stateStartup:
		m.state = stateInput
		m.updateSizes()
		return m, nil

	case stateSessionPicker:
		return m.handlePickerKey(k)

	case stateExtensions:
		if k.Type == tea.KeyEsc {
			m.extPanel.Visible = false
			m.state = stateInput
		}
		return m, nil

	case stateProcessing:
		return m.handleProcessingKey(k)

	case stateConfirm:
		return m.handleConfirmKey(k)

	case stateProvider:
		return m.handleProviderKey(k)

	default: // stateInput
		return m.handleInputKey(k)
	}
}

// handleProviderKey routes keys for the provider/model/api-key wizard overlay.
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
		case tea.KeyLeft, tea.KeyPgUp:
			if m.provPanel.modelIdx > 0 {
				m.provPanel.modelIdx -= m.provPanel.pageSize
				if m.provPanel.modelIdx < 0 {
					m.provPanel.modelIdx = 0
				}
			}
			return m, nil
		case tea.KeyRight, tea.KeyPgDown:
			if last := len(m.provPanel.models) - 1; last > 0 {
				m.provPanel.modelIdx += m.provPanel.pageSize
				if m.provPanel.modelIdx > last {
					m.provPanel.modelIdx = last
				}
			}
			return m, nil
		case tea.KeyEnter:
			m.enterKeyStep()
			return m, nil
		}
		return m, nil

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
	}
	return m, nil
}

// handleInputKey handles keys while composing.
func (m *Model) handleInputKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlT {
		m.toggleTaskSidebar()
		return m, nil
	}

	if k.Type == tea.KeyCtrlO {
		m.toolsExpanded = !m.toolsExpanded
		m.rebuildViewport()
		return m, nil
	}

	// Command panel (ephemeral output) intercepts keys while visible.
	if m.cmdPanel.visible {
		switch m.cmdPanel.kind {
		case cmdPanelSelect:
			switch k.Type {
			case tea.KeyUp:
				m.cmdPanel.moveUp()
				return m, nil
			case tea.KeyDown:
				m.cmdPanel.moveDown()
				return m, nil
			case tea.KeyEnter:
				return m.execCmdPanel()
			case tea.KeyEsc:
				m.closePanel()
				return m, nil
			}
			// MCP management shortcuts: c (close), r (reconnect), q (quit panel).
			if m.cmdPanel.action == actionMCPManage && k.Type == tea.KeyRunes && len(k.Runes) == 1 {
				switch k.Runes[0] {
				case 'q':
					m.closePanel()
					return m, nil
				case 'c', 'r':
					if m.cmdPanel.selected >= 0 && m.cmdPanel.selected < len(m.cmdPanel.items) {
						name := m.cmdPanel.items[m.cmdPanel.selected].value
						op := "close"
						if k.Runes[0] == 'r' {
							op = "reconnect"
						}
						m.closePanel()
						return m, func() tea.Msg {
							var err error
							if op == "close" {
								err = m.mcpRegistry.CloseServer(name)
							} else {
								err = m.mcpRegistry.Reconnect(name)
							}
							return mcpActionMsg{name: name, op: op, err: err}
						}
					}
				}
			}
		case cmdPanelText:
			if k.Type == tea.KeyEsc || k.Type == tea.KeyEnter {
				m.closePanel()
				return m, nil
			}
		}
		// Any other key dismisses the panel and falls through to composing.
		m.closePanel()
	}

	// Slash menu navigation takes precedence when open.
	if m.menuVisible {
		switch k.Type {
		case tea.KeyUp:
			if m.menuSelected > 0 {
				m.menuSelected--
			}
			return m, nil
		case tea.KeyDown:
			if m.menuSelected < len(menuMatches(m.input.Value()))-1 {
				m.menuSelected++
			}
			return m, nil
		case tea.KeyTab:
			return m.acceptMenu()
		case tea.KeyEsc:
			m.menuVisible = false
			return m, nil
		}
	}

	// File mention menu navigation when open (Tab/Enter insert the path).
	if m.fileMenuVisible {
		switch k.Type {
		case tea.KeyUp:
			if m.fileMenuSelected > 0 {
				m.fileMenuSelected--
			}
			return m, nil
		case tea.KeyDown:
			if m.fileMenuSelected < len(m.fileMenuItems)-1 {
				m.fileMenuSelected++
			}
			return m, nil
		case tea.KeyTab, tea.KeyEnter:
			return m.acceptFileMenu()
		case tea.KeyEsc:
			m.fileMenuVisible = false
			return m, nil
		}
	}

	// Suggestion accept: Tab on an empty composer (menu closed) accepts the
	// ghost text. Tab precedence: slash menu first (above), then suggestion.
	if !m.menuVisible && m.suggestion != "" && k.Type == tea.KeyTab &&
		strings.TrimSpace(m.input.Value()) == "" {
		m.acceptSuggestion()
		return m, nil
	}

	switch k.Type {
	case tea.KeyEnter:
		if m.menuVisible {
			return m.acceptMenu()
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		text = m.paste.expand(text)
		// Block submission when an @file mention points at a missing file.
		if !strings.HasPrefix(text, "/") {
			if missing := validateMentions(m.cwd, text); len(missing) > 0 {
				m.showTextPanel("attach", styleError().Render("file not found: "+strings.Join(missing, ", ")))
				return m, nil
			}
		}
		m.input.Reset()
		m.menuVisible = false
		m.menuSelected = 0
		m.fileMenuVisible = false
		m.history.add(text)
		return m, m.submit(text)

	case tea.KeyUp:
		if prev := m.history.prev(); prev != "" {
			m.input.textarea.SetValue(prev)
		}
		return m, nil

	case tea.KeyDown:
		m.input.textarea.SetValue(m.history.next())
		return m, nil
	}

	// Default: let the textarea consume the key, then update menu visibility.
	// Any non-Tab keystroke dismisses the ghost suggestion.
	m.suggestion = ""
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.input.SyncHeight()
	m.refreshMenu()
	m.maybeStashPaste()
	return m, cmd
}

// handleProcessingKey handles keys while the agent runs.
func (m *Model) handleProcessingKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.agent.Abort()
		m.addSystem(styleDim().Render("interrupted"))
		return m, nil
	case tea.KeyCtrlO:
		m.toolsExpanded = !m.toolsExpanded
		m.rebuildViewport()
		return m, nil
	case tea.KeyCtrlT:
		m.toggleTaskSidebar()
		return m, nil
	case tea.KeyEnter:
		// Follow-up while processing: steer the running agent.
		text := strings.TrimSpace(m.input.Value())
		if text != "" {
			m.input.Reset()
			m.addUser(text)
			m.agent.Steer(models.UserMessage(text))
		}
		return m, nil
	}
	// Allow composing a follow-up without submitting.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.input.SyncHeight()
	return m, cmd
}

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
		res := m.confirm.confirm()
		return m, func() tea.Msg { return confirmResponseMsg{allow: res.Allow, scope: res.Scope} }
	case tea.KeyEsc:
		return m, func() tea.Msg { return confirmResponseMsg{allow: false, scope: agent.ScopeDeny} }
	case tea.KeyRunes:
		switch strings.ToLower(string(k.Runes)) {
		case "y":
			return m, func() tea.Msg { return confirmResponseMsg{allow: true, scope: agent.ScopeOnce} }
		case "n":
			return m, func() tea.Msg { return confirmResponseMsg{allow: false, scope: agent.ScopeDeny} }
		}
	}

	// Let the viewport handle scroll keys (PgUp/PgDn/Up/Down/Home/End) so the
	// user can review logs while the prompt is waiting.
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(k)
	return m, cmd
}

// submit dispatches a user submission: skill trigger, slash command, or prompt.
func (m *Model) submit(text string) tea.Cmd {
	// Manual skill trigger ("/skill:name args") takes precedence over the
	// generic slash dispatch since it is also slash-prefixed.
	if name, rest, ok := skills.ParseManualTrigger(text); ok {
		return m.handleSkillTrigger(name, rest)
	}
	if strings.HasPrefix(text, "/") {
		return m.dispatchSlash(text)
	}
	// Auto-detect a matching skill for plain prompts.
	if m.autoDetectEnabled() {
		if score, ok := skills.AutoDetect(text, m.skills); ok {
			return m.handleSkillTrigger(score.Skill.Name, text)
		}
	}
	return m.startPrompt(text)
}

// startPrompt records the user block and kicks off the agent. @file mentions
// are kept verbatim for the agent except ~ which is expanded to an absolute
// path (the read tool cannot expand ~).
func (m *Model) startPrompt(text string) tea.Cmd {
	m.addUser(text)
	m.state = stateProcessing
	m.input.SetProcessing(true)
	m.errMsg = ""
	return tea.Batch(
		submitPromptCmd(m.agent, m.session, expandHomeMentions(text)),
		spinnerTick(),
	)
}

// onAgentDone returns the model to the input state and persists the session.
func (m *Model) onAgentDone(err error) {
	m.state = stateInput
	m.input.SetProcessing(false)
	if err != nil {
		m.addSystem(styleError().Render("error: " + err.Error()))
	}
	m.persistSession()
	m.updateSuggestion()
}

// refreshMenu toggles the slash and @file menus based on the current input.
func (m *Model) refreshMenu() {
	val := m.input.Value()
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") && !strings.Contains(val, "\n") {
		m.menuVisible = true
		if m.menuSelected >= len(menuMatches(val)) {
			m.menuSelected = 0
		}
	} else {
		m.menuVisible = false
		m.menuSelected = 0
	}

	if partial, ok := activeMention(val); ok && !m.menuVisible {
		m.fileMenuItems = fileMatches(m.cwd, partial)
		m.fileMenuVisible = len(m.fileMenuItems) > 0
		if m.fileMenuSelected >= len(m.fileMenuItems) {
			m.fileMenuSelected = 0
		}
	} else {
		m.fileMenuVisible = false
		m.fileMenuSelected = 0
		m.fileMenuItems = nil
	}
}

// acceptFileMenu replaces the in-progress @mention with the selected file path.
func (m *Model) acceptFileMenu() (*Model, tea.Cmd) {
	val := m.input.Value()
	if m.fileMenuSelected < len(m.fileMenuItems) {
		if at := strings.LastIndex(val, "@"); at >= 0 {
			m.input.textarea.SetValue(val[:at] + "@" + m.fileMenuItems[m.fileMenuSelected] + " ")
		}
	}
	m.fileMenuVisible = false
	m.fileMenuSelected = 0
	m.input.SyncHeight()
	return m, nil
}

// acceptMenu fills the composer with the selected command.
func (m *Model) acceptMenu() (*Model, tea.Cmd) {
	matches := menuMatches(m.input.Value())
	if m.menuSelected < len(matches) {
		m.input.textarea.SetValue("/" + matches[m.menuSelected].entry.Name + " ")
	}
	m.menuVisible = false
	return m, nil
}

// maybeStashPaste folds an oversized paste into a placeholder.
func (m *Model) maybeStashPaste() {
	val := m.input.Value()
	if m.paste.shouldStash(val) {
		placeholder := m.paste.stash(val)
		m.input.textarea.SetValue(placeholder)
	}
}

// forwardMsg routes non-key messages to the active overlay or composer.
func (m *Model) forwardMsg(msg tea.Msg) (*Model, tea.Cmd) {
	switch m.state {
	case stateSessionPicker:
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

// handlePickerKey handles keys while the session picker overlay is active.
func (m *Model) handlePickerKey(k tea.KeyMsg) (*Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.picker.Hide()
		m.state = stateInput
		return m, nil
	case tea.KeyEnter:
		if sel := m.picker.Selected(); sel != nil {
			m.loadSession(sel)
		}
		m.picker.Hide()
		m.state = stateInput
		return m, nil
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(k)
	return m, cmd
}

// dispatchSlash executes a slash command and returns any follow-up cmd.
func (m *Model) dispatchSlash(text string) tea.Cmd {
	sc, ok := parseSlashCommand(text)
	if !ok {
		return nil
	}
	entry, found := findCommand(sc.Name)
	if !found {
		m.showTextPanel(sc.Name, styleError().Render("unknown command: /"+sc.Name))
		return nil
	}
	switch entry.Name {
	case "help":
		m.showTextPanel("help", formatCommandHelp())
	case "new":
		m.blocks = nil
		m.rebuildViewport()
	case "sessions":
		m.openSessionPicker()
	case "extensions":
		m.extPanel.Visible = true
		m.state = stateExtensions
	case "mcp":
		m.openMCPPanel()
	case "tools":
		m.toolsExpanded = !m.toolsExpanded
		m.rebuildViewport()
	case "tasks":
		m.toggleTaskSidebar()
	case "mode":
		m.switchMode(strings.TrimSpace(sc.Args))
	case "modes":
		m.openModePanel()
	case "provider":
		m.openProviderPanel()
	case "status":
		m.showTextPanel("status", m.statusText())
	case "save":
		m.saveCheckpoint()
	case "restore":
		m.restoreCheckpoint()
	case "checkpoints":
		m.listCheckpoints()
	case "skill":
		m.openSkillPanel()
	case "retry":
		return m.retryLast()
	case "quit":
		return tea.Quit
	default:
		m.showTextPanel(entry.Name, styleError().Render("unhandled command: /"+entry.Name))
	}
	return nil
}

// switchMode changes the agent mode if the runner supports it. An empty mode
// opens the mode selection panel; a named mode switches silently (the status
// line reflects the active mode).
func (m *Model) switchMode(mode string) {
	if mode == "" {
		m.openModePanel()
		return
	}
	if sw, ok := m.agent.(ModeSwitcher); ok {
		m.agent = sw.WithMode(mode)
		m.closePanel()
	} else {
		m.showTextPanel("mode", styleError().Render("agent does not support modes"))
	}
}

// showTextPanel displays read-only command output above the composer.
func (m *Model) showTextPanel(title, text string) {
	m.cmdPanel = cmdPanel{visible: true, kind: cmdPanelText, title: title, text: text}
	m.updateSizes()
}

// closePanel hides the command panel and reclaims its rows for the viewport.
func (m *Model) closePanel() {
	if m.cmdPanel.visible {
		m.cmdPanel = cmdPanel{}
		m.updateSizes()
	}
}

// openModePanel shows the available agent modes as a selection box.
func (m *Model) openModePanel() {
	if m.modeManager == nil {
		m.showTextPanel("modes", styleDim().Render("no modes loaded"))
		return
	}
	var items []cmdPanelItem
	for _, mode := range m.modeManager.List() {
		items = append(items, cmdPanelItem{label: mode.Name, desc: mode.Description, value: mode.Name})
	}
	if len(items) == 0 {
		m.showTextPanel("modes", styleDim().Render("no modes loaded"))
		return
	}
	m.cmdPanel = cmdPanel{visible: true, kind: cmdPanelSelect, title: "modes", items: items, action: actionSwitchMode}
	m.updateSizes()
}

// openSkillPanel shows the loaded skills as a selection box.
func (m *Model) openSkillPanel() {
	if len(m.skills) == 0 {
		m.showTextPanel("skill", styleDim().Render("no skills loaded"))
		return
	}
	var items []cmdPanelItem
	for _, s := range m.skills {
		items = append(items, cmdPanelItem{label: s.Name, desc: s.WhenToUse, value: s.Name})
	}
	m.cmdPanel = cmdPanel{visible: true, kind: cmdPanelSelect, title: "skill", items: items, action: actionTriggerSkill}
	m.updateSizes()
}

// openMCPPanel shows configured MCP servers with close/reconnect shortcuts.
func (m *Model) openMCPPanel() {
	if m.mcpRegistry == nil {
		m.showTextPanel("mcp", styleDim().Render("no MCP registry"))
		return
	}
	servers := m.mcpRegistry.Servers()
	if len(servers) == 0 {
		m.showTextPanel("mcp", styleDim().Render("no MCP servers configured"))
		return
	}
	var items []cmdPanelItem
	for _, s := range servers {
		desc := fmt.Sprintf("%s · not connected", s.Transport)
		if s.Connected {
			desc = fmt.Sprintf("%s · connected · %d tools", s.Transport, s.ToolCount)
		}
		if s.Error != "" {
			desc = fmt.Sprintf("%s · %s", s.Transport, s.Error)
		}
		items = append(items, cmdPanelItem{label: s.Name, desc: desc, value: s.Name})
	}
	m.cmdPanel = cmdPanel{visible: true, kind: cmdPanelSelect, title: "mcp (c=close r=reconnect q=quit)", items: items, action: actionMCPManage}
	m.updateSizes()
}

// execCmdPanel runs the selected row's action and closes the panel.
func (m *Model) execCmdPanel() (*Model, tea.Cmd) {
	p := m.cmdPanel
	m.closePanel()
	if p.kind != cmdPanelSelect || p.selected < 0 || p.selected >= len(p.items) {
		return m, nil
	}
	switch p.action {
	case actionSwitchMode:
		m.switchMode(p.items[p.selected].value)
	case actionTriggerSkill:
		return m, m.handleSkillTrigger(p.items[p.selected].value, "")
	case actionMCPManage:
		name := p.items[p.selected].value
		return m, func() tea.Msg {
			err := m.mcpRegistry.Reconnect(name)
			return mcpActionMsg{name: name, op: "reconnect", err: err}
		}
	}
	return m, nil
}

// statusText renders a compact status summary line.
func (m *Model) statusText() string {
	parts := []string{
		"mode: " + m.modeLabel(),
		"model: " + m.model,
		"cwd: " + m.cwd,
		"session: " + truncate(m.session.SessionID(), 12),
	}
	if len(m.capabilities) > 0 {
		parts = append(parts, "caps: "+strings.Join(m.capabilities, ","))
	}
	return styleDim().Render(strings.Join(parts, "  ·  "))
}

// retryLast prunes the final assistant turn and re-runs the last user prompt.
func (m *Model) retryLast() tea.Cmd {
	msgs := m.agent.AllMessages()
	var lastUser string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			lastUser = msgs[i].Text()
			break
		}
	}
	if lastUser == "" {
		m.addSystem(styleDim().Render("nothing to retry"))
		return nil
	}
	var pruned []models.AgentMessage
	for _, msg := range msgs {
		pruned = append(pruned, msg)
		if msg.Role == models.RoleUser && msg.Text() == lastUser {
			break
		}
	}
	m.agent.SetMessages(pruned)
	return m.startPrompt(lastUser)
}

// loadSession replaces history with a stored session's messages and rebuilds the
// task sidebar from the latest todo_write call in that history.
func (m *Model) loadSession(sess *session.Session) {
	if sess == nil {
		return
	}
	msgs := sess.ActiveMessages()
	m.blocks = blocksFromMessages(msgs)
	m.tasks = tasksFromMessages(msgs)
	m.agent.SetMessages(msgs)
	m.updateSizes()
}

// openSessionPicker switches to the picker overlay.
func (m *Model) openSessionPicker() {
	var cur *session.Session
	if s, ok := m.session.(*session.Session); ok {
		cur = s
	}
	m.picker = NewSessionPicker(m.store, m.cwd, "select", cur)
	m.state = stateSessionPicker
}

// persistSession mirrors the agent's full context window (assistant and
// tool_result messages, not just the user prompts appended at submit time) into
// the session and writes it to disk. No-op if not a *session.Session.
func (m *Model) persistSession() {
	if sess, ok := m.session.(*session.Session); ok {
		_ = sess.AppendMissing(m.agent.AllMessages())
	}
}
