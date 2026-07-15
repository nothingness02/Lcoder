package tui

import "strings"

// saveCheckpoint snapshots the agent via the CheckpointSource interface and
// persists it under the current session ID.
func (m *Model) saveCheckpoint() {
	src, ok := m.agent.(CheckpointSource)
	if !ok {
		m.showTextPanel("save", styleError().Render("agent does not support checkpoints"))
		return
	}
	if m.checkpointStore == nil {
		m.showTextPanel("save", styleError().Render("no checkpoint store configured"))
		return
	}
	cp, err := src.Checkpoint()
	if err != nil {
		m.showTextPanel("save", styleError().Render("checkpoint failed: "+err.Error()))
		return
	}
	id := m.session.SessionID()
	if err := m.checkpointStore.Save(id, cp); err != nil {
		m.showTextPanel("save", styleError().Render("save failed: "+err.Error()))
		return
	}
	m.showTextPanel("save", styleDim().Render("checkpoint saved: "+id))
}

// restoreCheckpoint loads the checkpoint for the current session and applies it
// via the CheckpointTarget interface, then refreshes the viewport from the
// restored agent messages.
func (m *Model) restoreCheckpoint() {
	tgt, ok := m.agent.(CheckpointTarget)
	if !ok {
		m.showTextPanel("restore", styleError().Render("agent does not support checkpoints"))
		return
	}
	if m.checkpointStore == nil {
		m.showTextPanel("restore", styleError().Render("no checkpoint store configured"))
		return
	}
	id := m.session.SessionID()
	cp, err := m.checkpointStore.Load(id)
	if err != nil {
		m.showTextPanel("restore", styleError().Render("load failed: "+err.Error()))
		return
	}
	if err := tgt.Restore(cp); err != nil {
		m.showTextPanel("restore", styleError().Render("restore failed: "+err.Error()))
		return
	}
	msgs := m.agent.AllMessages()
	m.blocks = blocksFromMessages(msgs)
	m.components = componentsFromBlocks(m.blocks)
	m.tasks = tasksFromMessages(msgs)
	m.updateSizes()
	m.showTextPanel("restore", styleDim().Render("checkpoint restored: "+id))
}

// listCheckpoints shows the identifiers of all saved checkpoints.
func (m *Model) listCheckpoints() {
	if m.checkpointStore == nil {
		m.showTextPanel("checkpoints", styleError().Render("no checkpoint store configured"))
		return
	}
	ids, err := m.checkpointStore.List()
	if err != nil {
		m.showTextPanel("checkpoints", styleError().Render("list failed: "+err.Error()))
		return
	}
	if len(ids) == 0 {
		m.showTextPanel("checkpoints", styleDim().Render("no checkpoints saved"))
		return
	}
	m.showTextPanel("checkpoints", styleDim().Render(strings.Join(ids, "\n")))
}
