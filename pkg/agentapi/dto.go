package agentapi

import "github.com/lcoder/lcoder/pkg/contextmgr"

// TokenBudget is re-exported so protocol consumers (the TUI's provider panel)
// can call SwitchModel without importing pkg/contextmgr directly.
type TokenBudget = contextmgr.TokenBudget

// ModeInfo is the UI-facing description of one agent mode (the /modes panel).
// Modes are static config loaded at startup, so a snapshot suffices.
type ModeInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SessionInfo is the read-only session metadata the session picker lists.
// Title is the display title (explicit title, latest user message, or ID).
type SessionInfo struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	MessageCount int    `json:"message_count"`
	CWD          string `json:"cwd"`
	// Subagent marks subagent journals; pickers filter them out.
	Subagent bool `json:"subagent,omitempty"`
}

// ContextStats is the structured form of the context manager's token
// accounting. It replaces the old Stats() map[string]int with its magic keys;
// the field set covers every fixed key that map could carry.
type ContextStats struct {
	// Total is the heuristic token estimate across all blocks.
	Total int `json:"total"`
	// BudgetMax is the hard context window cap.
	BudgetMax int `json:"budget_max"`
	// BudgetTarget is the soft target the compactor aims for.
	BudgetTarget int `json:"budget_target"`
	// BudgetOutputReserve is the token reserve for model output.
	BudgetOutputReserve int `json:"budget_output_reserve"`
	// DropLimit is the threshold at which blocks start getting dropped.
	DropLimit int `json:"drop_limit"`
	// Real* are the provider-reported prompt-token accounting, present only
	// after a turn has reported usage (RealPromptTotal > 0).
	RealInput         int `json:"real_input,omitempty"`
	RealCacheRead     int `json:"real_cache_read,omitempty"`
	RealCacheCreation int `json:"real_cache_creation,omitempty"`
	RealPromptTotal   int `json:"real_prompt_total,omitempty"`
	// CompactionLevel is the current multi-level compaction pressure tier
	// (0=none..3=reactive).
	CompactionLevel int `json:"compaction_level"`
	// Blocks carries the per-block token estimates, keyed "kind:name".
	Blocks map[string]int `json:"blocks,omitempty"`
}

// CheckpointInfo describes one stored checkpoint entry, as reported by
// CoreAPI.ListCheckpoints. The checkpoint store keys checkpoints by session
// identifier, so ID is currently that session identifier.
type CheckpointInfo struct {
	ID string `json:"id"`
}
