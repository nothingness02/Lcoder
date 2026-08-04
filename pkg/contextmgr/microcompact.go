package contextmgr

import (
	"fmt"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

// MicroCompactConfig controls the mechanical, projection-layer tool-result
// trimming. It mirrors kimi-code's MicroCompactionConfig (micro.ts):
// oversized tool results older than a rolling cutoff are replaced with a short
// placeholder in the outgoing request only — blocks keep the full content, so
// the operation is lossless and reversible. Disabled by default; when disabled
// every method is a no-op.
type MicroCompactConfig struct {
	// Enabled is the master switch (yaml: micro_compact). When false all
	// detection and replacement is skipped with zero overhead.
	Enabled bool
	// KeepRecent is the number of newest messages never subject to trimming
	// (yaml: micro_compact_keep_recent).
	KeepRecent int
	// MinChars is the minimum text length of a tool result before it becomes
	// eligible for trimming (yaml: micro_compact_min_chars).
	MinChars int
	// CacheMissedMs is the minimum age of the last assistant message before a
	// trim is considered. Trimming only pays off when the provider's prompt
	// cache has already gone cold — otherwise the replacement would discard
	// cached prefix value for nothing (yaml: micro_compact_cache_missed_ms).
	CacheMissedMs int64
	// MinUsageRatio is the minimum fraction of the effective input window that
	// must be occupied before a trim is considered (yaml: micro_compact_min_usage).
	MinUsageRatio float64
}

// DefaultMicroCompact returns the shipped defaults: disabled, with the
// kimi-derived thresholds (keep 20 newest, >=400 chars, >=1h cache cold,
// >=50% usage).
func DefaultMicroCompact() MicroCompactConfig {
	return MicroCompactConfig{
		Enabled:        false,
		KeepRecent:     20,
		MinChars:       400,
		CacheMissedMs:  3_600_000, // 1h
		MinUsageRatio:  0.5,
	}
}

// microCompactMarkerPrefix labels a trimmed tool result in the outgoing copy.
// It is not persisted, so it never leaks into sessions or checkpoints.
func microCompactMarker(contentLen int, name string) string {
	return fmt.Sprintf("[tool result cleared: %d chars — %s]", contentLen, name)
}

// MicroCompactor implements the mechanical micro-compaction policy. It keeps a
// rolling cutoff index: messages below the cutoff are eligible for trimming.
// The cutoff only ever advances (never recedes) until Reset, matching
// kimi-code's unidirectional cutoff.
//
// All methods must be called with the manager's lock held (BuildTurnRequest /
// fold / restore already hold it).
type MicroCompactor struct {
	cfg    MicroCompactConfig
	cutoff int
}

// NewMicroCompactor builds a compactor from cfg. A zero-value cfg (not
// DefaultMicroCompact) yields a disabled compactor.
func NewMicroCompactor(cfg MicroCompactConfig) *MicroCompactor {
	if cfg.KeepRecent <= 0 {
		cfg.KeepRecent = DefaultMicroCompact().KeepRecent
	}
	if cfg.MinChars <= 0 {
		cfg.MinChars = DefaultMicroCompact().MinChars
	}
	if cfg.CacheMissedMs <= 0 {
		cfg.CacheMissedMs = DefaultMicroCompact().CacheMissedMs
	}
	if cfg.MinUsageRatio <= 0 {
		cfg.MinUsageRatio = DefaultMicroCompact().MinUsageRatio
	}
	return &MicroCompactor{cfg: cfg}
}

// Enabled reports whether the compactor is active.
func (c *MicroCompactor) Enabled() bool {
	return c != nil && c.cfg.Enabled
}

// Status returns a short human-readable status for /status echo: "on" when
// enabled (with the current cutoff when it has advanced), empty when disabled.
func (c *MicroCompactor) Status() string {
	if !c.Enabled() {
		return ""
	}
	if c.cutoff > 0 {
		return fmt.Sprintf("on (cutoff %d)", c.cutoff)
	}
	return "on"
}

// lastAssistantAt returns the Unix-millis timestamp of the newest assistant
// message in msgs, or (0, false) when there is none.
func lastAssistantAt(msgs []models.AgentMessage) (int64, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleAssistant {
			return msgs[i].Timestamp, true
		}
	}
	return 0, false
}

// Detect advances the cutoff when the trim conditions hold (all must pass):
//   - enabled;
//   - the cache is cold: the newest assistant message is older than
//     CacheMissedMs;
//   - the context is at least MinUsageRatio full of the effective input window.
//
// It returns the previous cutoff (or -1 when it did not advance) for logging.
// It never touches messages — only the rolling cutoff index.
func (c *MicroCompactor) Detect(now time.Time, msgs []models.AgentMessage, effectiveInput int, currentTokens int) int {
	if !c.Enabled() {
		return -1
	}
	lastAt, ok := lastAssistantAt(msgs)
	if !ok {
		return -1 // never ran: nothing to trim
	}
	if now.UnixMilli()-lastAt < c.cfg.CacheMissedMs {
		return -1 // cache still warm; trimming would discard its value
	}
	if effectiveInput <= 0 || float64(currentTokens)/float64(effectiveInput) < c.cfg.MinUsageRatio {
		return -1 // context not full enough to bother
	}
	next := len(msgs) - c.cfg.KeepRecent
	if next <= c.cutoff {
		return -1 // cutoff never recedes
	}
	prev := c.cutoff
	c.cutoff = next
	return prev
}

// Apply replaces eligible tool results in the outgoing request copy. It
// mutates the passed slice (a per-request copy, never the stored blocks):
// messages below the cutoff whose content is a ToolResultContent with text
// length >= MinChars get their inner Content swapped for a short marker. The
// ToolResultContent shell (ToolCallID, Name, IsError, Details) is preserved,
// so the tool_use/tool_result pairing stays intact on the wire. It returns
// the number replaced.
//
// A replaced message keeps a single ToolResultContent whose text is the
// marker; the original text length is embedded so the volume information
// survives.
func (c *MicroCompactor) Apply(msgs []models.AgentMessage) int {
	if !c.Enabled() || c.cutoff <= 0 {
		return 0
	}
	replaced := 0
	for i := 0; i < len(msgs) && i < c.cutoff; i++ {
		msg := &msgs[i]
		if msg.Role != models.RoleToolResult {
			continue
		}
		var tr models.ToolResultContent
		found := false
		for _, part := range msg.Content {
			if r, ok := part.(models.ToolResultContent); ok {
				tr = r
				found = true
				break
			}
		}
		if !found {
			continue
		}
		runes := len([]rune(tr.Text()))
		if runes < c.cfg.MinChars {
			continue
		}
		tr.Content = []models.ContentPart{models.TextContent{Text: microCompactMarker(runes, tr.Name)}}
		msg.Content = []models.ContentPart{tr}
		replaced++
	}
	return replaced
}

// Reset clears the cutoff (to max(0, min(cutoff, maxCutoff)), matching
// kimi's reset(maxCutoff)). Call after a full compaction or a restore, when
// the message indexes no longer refer to the same content.
func (c *MicroCompactor) Reset(maxCutoff int) {
	if c == nil {
		return
	}
	if maxCutoff < 0 {
		maxCutoff = 0
	}
	if c.cutoff > maxCutoff {
		c.cutoff = maxCutoff
	}
}
