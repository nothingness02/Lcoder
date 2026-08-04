package contextmgr

import (
	"encoding/json"
	"fmt"

	"github.com/lcoder/lcoder/pkg/models"
)

// ManagerState is a serializable snapshot of a Manager's runtime state.
type ManagerState struct {
	Budget             TokenBudget
	Blocks             []BlockState
	EphemeralReminders []string
	LastUsage          *RealUsage
	CachePolicy        string
	MinRecent          int
}

// BlockState is a serializable mirror of Block.
type BlockState struct {
	Kind             BlockKind
	Name             string
	Priority         int
	Stability        Stability
	Messages         []models.AgentMessage
	Metadata         map[string]any
	CacheHint        CacheHint
	LastModifiedTurn int
}

// Snapshot returns a deep copy of the manager's current state.
func (m *Manager) Snapshot() (*ManagerState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &ManagerState{
		Budget:             m.budget,
		CachePolicy:        string(m.cachePolicy),
		EphemeralReminders: append([]string(nil), m.ephemeralReminders...),
		Blocks:             make([]BlockState, 0, len(m.blocks)),
		MinRecent:          m.keepRecent,
	}

	if m.hasUsage {
		usage := m.lastUsage
		state.LastUsage = &usage
	}

	for _, b := range m.blocks {
		bs := BlockState{
			Kind:             b.Kind,
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        b.Stability,
			Messages:         append([]models.AgentMessage(nil), b.Messages...),
			Metadata:         copyMetadata(b.Metadata),
			CacheHint:        b.CacheHint,
			LastModifiedTurn: b.LastModifiedTurn,
		}
		for i := range bs.Messages {
			bs.Messages[i].Metadata = copyMetadata(bs.Messages[i].Metadata)
		}
		state.Blocks = append(state.Blocks, bs)
	}

	return state, nil
}

// SnapshotRuntime returns the manager's runtime state without copying block
// messages. It is intended for frequent, lightweight checkpoints where message
// history is persisted separately (e.g. by the session store).
func (m *Manager) SnapshotRuntime() (*ManagerState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &ManagerState{
		Budget:             m.budget,
		CachePolicy:        string(m.cachePolicy),
		EphemeralReminders: append([]string(nil), m.ephemeralReminders...),
		Blocks:             make([]BlockState, 0, len(m.blocks)),
		MinRecent:          m.keepRecent,
	}

	if m.hasUsage {
		usage := m.lastUsage
		state.LastUsage = &usage
	}

	for _, b := range m.blocks {
		state.Blocks = append(state.Blocks, BlockState{
			Kind:             b.Kind,
			Name:             b.Name,
			Priority:         b.Priority,
			Stability:        b.Stability,
			Metadata:         copyMetadata(b.Metadata),
			CacheHint:        b.CacheHint,
			LastModifiedTurn: b.LastModifiedTurn,
		})
	}

	return state, nil
}

// Restore replaces the manager's state with the provided snapshot. If a block
// state does not carry messages, the existing block's messages are preserved and
// only metadata (priority, cache hint, last modified turn) is updated. This lets
// a lightweight runtime checkpoint be applied after session messages have been
// loaded separately.
func (m *Manager) Restore(state *ManagerState) error {
	if state == nil {
		return fmt.Errorf("contextmgr: cannot restore: nil state")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.estimator == nil || m.summarizer == nil || m.policy == nil {
		return fmt.Errorf("contextmgr: cannot restore: internal services (estimator/summarizer/policy) not wired")
	}

	m.budget = state.Budget
	m.cachePolicy = ParseCacheHintPolicy(state.CachePolicy)
	m.ephemeralReminders = append([]string(nil), state.EphemeralReminders...)
	if state.MinRecent > 0 {
		m.keepRecent = state.MinRecent
	}

	if state.LastUsage != nil {
		m.lastUsage = *state.LastUsage
		m.hasUsage = true
	} else {
		m.lastUsage = RealUsage{}
		m.hasUsage = false
	}

	// A restore replaces the block contents wholesale; the micro-compact cutoff
	// no longer refers to the same messages.
	if m.microCompact != nil {
		m.microCompact.Reset(0)
	}

	type blockKey struct {
		kind BlockKind
		name string
	}
	existing := make(map[blockKey]*Block, len(m.blocks))
	for _, b := range m.blocks {
		existing[blockKey{b.Kind, b.Name}] = b
	}

	for _, bs := range state.Blocks {
		key := blockKey{bs.Kind, bs.Name}
		b, ok := existing[key]
		if !ok {
			// Metadata-only snapshots should not create empty blocks.
			if len(bs.Messages) == 0 {
				continue
			}
			b = NewBlock(bs.Kind, bs.Name, bs.Stability, bs.Priority)
			existing[key] = b
			m.blocks = append(m.blocks, b)
		}
		if len(bs.Messages) > 0 {
			b.Messages = append([]models.AgentMessage(nil), bs.Messages...)
			for i := range b.Messages {
				b.Messages[i].Metadata = copyMetadata(b.Messages[i].Metadata)
			}
		}
		b.Metadata = copyMetadata(bs.Metadata)
		b.CacheHint = bs.CacheHint
		b.LastModifiedTurn = bs.LastModifiedTurn
		b.Priority = bs.Priority
		b.Stability = bs.Stability
	}

	return nil
}

// copyMetadata deep-copies a metadata map using JSON round-trip.
func copyMetadata(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	data, err := json.Marshal(src)
	if err == nil {
		var dst map[string]any
		if err := json.Unmarshal(data, &dst); err == nil {
			return dst
		}
	}
	// Fallback to a shallow copy when JSON serialization fails.
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
