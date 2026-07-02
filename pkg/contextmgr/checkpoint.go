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

// Restore replaces the manager's state with the provided snapshot.
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

	blocks := make([]*Block, 0, len(state.Blocks))
	for _, bs := range state.Blocks {
		b := NewBlock(bs.Kind, bs.Name, bs.Stability, bs.Priority)
		b.Messages = append([]models.AgentMessage(nil), bs.Messages...)
		for i := range b.Messages {
			b.Messages[i].Metadata = copyMetadata(b.Messages[i].Metadata)
		}
		b.Metadata = copyMetadata(bs.Metadata)
		b.CacheHint = bs.CacheHint
		b.LastModifiedTurn = bs.LastModifiedTurn
		blocks = append(blocks, b)
	}
	m.blocks = blocks

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
