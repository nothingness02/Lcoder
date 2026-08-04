package tui

import "strings"

// saveCheckpoint snapshots the agent state through the core, which persists it
// under the current session's checkpoint id.
func (m *Model) saveCheckpoint() {
	id, err := m.agent.SaveCheckpoint()
	if err != nil {
		m.showTextPanel("save", styleError().Render("checkpoint failed: "+err.Error()))
		return
	}
	m.showTextPanel("save", styleDim().Render("checkpoint saved: "+id))
}

// restoreCheckpoint loads the current session's checkpoint through the core,
// then refreshes the viewport from the restored agent messages.
func (m *Model) restoreCheckpoint() {
	id := m.agent.SessionID()
	if err := m.agent.RestoreCheckpoint(id); err != nil {
		if m.reportBusyErr("restore", err) {
			return
		}
		m.showTextPanel("restore", styleError().Render("restore failed: "+err.Error()))
		return
	}
	m.reloadFromCore()
	m.showTextPanel("restore", styleDim().Render("checkpoint restored: "+id))
}

// listCheckpoints shows the identifiers of all saved checkpoints.
func (m *Model) listCheckpoints() {
	infos, err := m.agent.ListCheckpoints()
	if err != nil {
		m.showTextPanel("checkpoints", styleError().Render("list failed: "+err.Error()))
		return
	}
	if len(infos) == 0 {
		m.showTextPanel("checkpoints", styleDim().Render("no checkpoints saved"))
		return
	}
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	m.showTextPanel("checkpoints", styleDim().Render(strings.Join(ids, "\n")))
}
