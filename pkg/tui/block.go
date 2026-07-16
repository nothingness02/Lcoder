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
	thinking string
	usage    *blockUsage
	expanded bool

	// tool extras
	toolName    string
	toolArgs    string
	toolErr     bool
	toolStart   time.Time
	toolRunning bool
	elapsed     time.Duration
	toolResult  string // full tool output shown in the Ctrl+O expanded view
}

type blockUsage struct {
	inputTokens  int
	outputTokens int
	totalTokens  int
	cost         float64
}
