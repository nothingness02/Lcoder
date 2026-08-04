package task

import (
	"fmt"
	"sync"
)

// Manager holds the agent's current task list and notifies subscribers on change.
type Manager struct {
	mu    sync.RWMutex
	tasks []Task
	subs  []func([]Task)
}

// NewManager creates an empty task manager.
func NewManager() *Manager {
	return &Manager{}
}

// List returns a snapshot of the current tasks.
func (m *Manager) List() []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Task, len(m.tasks))
	copy(out, m.tasks)
	return out
}

// Counts tallies tasks by status.
func (m *Manager) Counts() (done, inProgress, pending int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Counts(m.tasks)
}

// FormatReminder returns a gentle reminder string when there are unfinished
// tasks. It returns an empty string when there is no task or all are done.
// The caller decides how often to surface it (see the todo injector); the
// wording deliberately nudges rather than commands so a repeated reminder
// does not push the model into defensive behavior.
func (m *Manager) FormatReminder() string {
	m.mu.RLock()
	tasks := make([]Task, len(m.tasks))
	copy(tasks, m.tasks)
	m.mu.RUnlock()

	if len(tasks) == 0 {
		return ""
	}
	done, inProgress, pending := Counts(tasks)
	remaining := inProgress + pending
	if remaining == 0 {
		return ""
	}
	return fmt.Sprintf("This is a gentle reminder that you have %d unfinished todo item(s) (%d done). Keep them in mind as you work and update the list if it no longer matches what you are doing; ignore this if it is not applicable.", remaining, done)
}

// Subscribe registers a callback that receives a snapshot of tasks after each change.
// The callback is invoked without holding the lock.
func (m *Manager) Subscribe(fn func([]Task)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

func (m *Manager) notify() {
	m.mu.RLock()
	snapshot := make([]Task, len(m.tasks))
	copy(snapshot, m.tasks)
	subs := make([]func([]Task), len(m.subs))
	copy(subs, m.subs)
	m.mu.RUnlock()

	for _, fn := range subs {
		fn(snapshot)
	}
}

// Snapshot returns a serializable copy of the manager state.
func (m *Manager) Snapshot() ManagerState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tasks := make([]Task, len(m.tasks))
	copy(tasks, m.tasks)
	return ManagerState{Tasks: tasks}
}

// Restore replaces the manager state from a snapshot.
func (m *Manager) Restore(state ManagerState) error {
	for i, t := range state.Tasks {
		if t.Text == "" {
			return fmt.Errorf("task %d: text must not be empty", i)
		}
		if !validStatus(t.Status) {
			return fmt.Errorf("task %d: invalid status %q", i, t.Status)
		}
	}
	m.mu.Lock()
	m.tasks = make([]Task, len(state.Tasks))
	copy(m.tasks, state.Tasks)
	m.mu.Unlock()
	m.notify()
	return nil
}

// ReplaceAll replaces the current task list with the provided one.
// It reconciles against the old list: pending/in_progress tasks that are missing
// from the new list are automatically re-added, and warnings are returned.
// Completed tasks may be dropped.
func (m *Manager) ReplaceAll(tasks []Task) (reconciled []Task, warnings []string, err error) {
	for i, t := range tasks {
		if t.Text == "" {
			return nil, nil, fmt.Errorf("task %d: text must not be empty", i)
		}
		if !validStatus(t.Status) {
			return nil, nil, fmt.Errorf("task %d: invalid status %q", i, t.Status)
		}
	}

	m.mu.Lock()
	oldTasks := m.tasks
	m.mu.Unlock()

	newIndex := make(map[string]struct{}, len(tasks))
	reconciled = make([]Task, 0, len(tasks))
	for _, t := range tasks {
		newIndex[t.Text] = struct{}{}
		reconciled = append(reconciled, t)
	}

	for _, old := range oldTasks {
		if old.Status == StatusDone {
			continue
		}
		if _, ok := newIndex[old.Text]; !ok {
			reconciled = append(reconciled, old)
			warnings = append(warnings, fmt.Sprintf("re-added unfinished task: %q", old.Text))
		}
	}

	m.mu.Lock()
	m.tasks = reconciled
	m.mu.Unlock()
	m.notify()
	return reconciled, warnings, nil
}
