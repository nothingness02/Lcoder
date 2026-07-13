package contextmgr

import "context"

// CompactionLevel classifies context pressure into escalating tiers, mirroring
// the multi-stage compaction in Claude-Code-like agents: a proactive fold well
// before the window fills, a tighter preflight fold as it nears the limit, and a
// reactive fold once the prompt would overflow the effective input window.
type CompactionLevel int

const (
	CompactionNone      CompactionLevel = iota // below the proactive threshold
	CompactionProactive                        // >= 90% of effective input
	CompactionPreflight                        // >= 95% of effective input
	CompactionReactive                         // >= 100% (would overflow)
)

// String renders the level for snapshots and logs.
func (l CompactionLevel) String() string {
	switch l {
	case CompactionProactive:
		return "proactive"
	case CompactionPreflight:
		return "preflight"
	case CompactionReactive:
		return "reactive"
	default:
		return "none"
	}
}

// Compaction pressure thresholds as ratios of the effective input window.
const (
	proactiveRatio = 0.90
	preflightRatio = 0.95
	reactiveRatio  = 1.00
)

// PressureLevel maps a prompt-token total to a compaction level against the
// budget's effective input window (MaxTotal - ReserveOutput).
func (b TokenBudget) PressureLevel(total int) CompactionLevel {
	eff := b.EffectiveInput()
	if eff <= 0 {
		return CompactionNone
	}
	r := float64(total) / float64(eff)
	switch {
	case r >= reactiveRatio:
		return CompactionReactive
	case r >= preflightRatio:
		return CompactionPreflight
	case r >= proactiveRatio:
		return CompactionProactive
	default:
		return CompactionNone
	}
}

// minLeveledMessages is a short-session guard: conversations with fewer recent
// messages than this are never compacted, even under pressure — folding two or
// three messages saves nothing and loses fidelity.
const minLeveledMessages = 4

// keepForLevel returns how many recent messages survive the fold at each level:
// the hotter the pressure, the fewer messages are kept.
func (m *Manager) keepForLevel(level CompactionLevel) int {
	base := m.keepRecent
	if base < 1 {
		base = 1
	}
	switch level {
	case CompactionProactive:
		return base
	case CompactionPreflight:
		return max(1, base/2)
	case CompactionReactive:
		return 1
	default:
		return base
	}
}

// MaybeCompactLeveled commits a multi-level compaction at a turn boundary. It
// classifies the current prompt-token total (real provider usage when available,
// else the heuristic estimate) into a CompactionLevel and folds the older recent
// messages with a keep count scaled to that level. It returns the level it acted
// on, whether a fold was committed, and any (non-fatal) summarizer error. Below
// the proactive threshold, or for a short session, it is a no-op.
func (m *Manager) MaybeCompactLeveled() (CompactionLevel, bool, error) {
	if m.summarizer == nil {
		return CompactionNone, false, nil
	}
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) < minLeveledMessages {
		return CompactionNone, false, nil
	}
	level := m.budget.PressureLevel(m.currentTotalTokens())
	if level == CompactionNone {
		return CompactionNone, false, nil
	}
	committed, err := m.foldOlder(context.Background(), m.keepForLevel(level))
	return level, committed, err
}
