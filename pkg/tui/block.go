package tui

import (
	"time"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

// block is one rendered unit of conversation history.
type block struct {
	kind components.BlockKind
	id   string // message ID or tool-call ID (for in-place updates)
	raw  string // user text / assistant markdown / tool result content

	// user extras
	attachments []string // @file mention basenames shown under the bar

	// assistant extras
	thinking      string
	usage         *blockUsage
	expanded      bool
	thinkingStart time.Time // first thinking delta of this message
	thinkingSecs  float64   // recorded at commit; >0 marks the trace complete

	// tool extras
	toolName    string
	toolArgs    string
	toolErr     bool
	toolStart   time.Time
	toolRunning bool
	elapsed     time.Duration
	toolResult  string // full tool output shown in the Ctrl+O expanded view
	toolChip    string // compact result statistic shown in the header (e.g. "12 lines")

	// subagent extras: nested activity mirrored from a child agent (see
	// events.SubagentActivityEvent). lines are completed activity entries;
	// tail is the in-flight text delta stream; live while the child runs.
	subagentLines []string
	subagentTail  string
	subagentLive  bool

	// subagentChildren tracks per-child state for the group display (kimi's
	// AgentGroup): populated from mirrored activity keyed by agent id.
	subagentChildren map[string]*subagentChild
	subagentOrder    []string
}

// subagentChild is one row in the subagent group display.
type subagentChild struct {
	profile string
	status  string // "running" | "completed" | "timeout" | "failed"
	tools   int
	started time.Time
	elapsed time.Duration
}

type blockUsage struct {
	inputTokens  int
	outputTokens int
	totalTokens  int
	cost         float64
}
