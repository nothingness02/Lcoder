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

// keepTokensForLevel returns the kept-tail token budget for each pressure
// level: the hotter the pressure, the smaller the surviving tail. The budget
// is capped at 30% of the effective input window so the kept tail cannot
// immediately re-trigger compaction next turn.
func (m *Manager) keepTokensForLevel(level CompactionLevel) int {
	base := m.keepRecentTokens
	if base <= 0 {
		base = defaultKeepRecentTokens
	}
	var budget int
	switch level {
	case CompactionProactive:
		budget = base
	case CompactionPreflight:
		budget = base / 2
	case CompactionReactive:
		budget = base / 5
	default:
		budget = base
	}
	if eff := m.budget.EffectiveInput(); eff > 0 {
		if cap30 := eff * 30 / 100; budget > cap30 {
			budget = cap30
		}
	}
	if budget < 256 {
		budget = 256
	}
	return budget
}

// PendingCompaction reports the level MaybeCompactLeveled would commit at now,
// or CompactionNone. It mirrors the guards of MaybeCompactLeveled without
// folding anything, so callers can signal "compaction starting" before the
// blocking call.
func (m *Manager) PendingCompaction() CompactionLevel {
	if m.summarizer == nil {
		return CompactionNone
	}
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) < minLeveledMessages {
		return CompactionNone
	}
	return m.budget.PressureLevel(m.currentTotalTokens())
}

// MaybeCompactLeveled commits a multi-level compaction at a turn boundary.
func (m *Manager) MaybeCompactLeveled(ctx context.Context) (CompactionLevel, FoldResult, error) {
	if m.summarizer == nil {
		return CompactionNone, FoldResult{}, nil
	}
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) < minLeveledMessages {
		return CompactionNone, FoldResult{}, nil
	}
	level := m.budget.PressureLevel(m.currentTotalTokens())
	if level == CompactionNone {
		return CompactionNone, FoldResult{}, nil
	}
	res, err := m.foldOlder(ctx, level)
	return level, res, err
}
