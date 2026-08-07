package host

import (
	"encoding/json"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// usageLedgerCustomType is the custom-entry type of the per-turn usage
// ledger. Entries live on the session branch like any message, so the ledger
// follows fork/retry semantics: a turn abandoned by /retry keeps its entry on
// the old branch and is excluded from the active branch's aggregates.
const usageLedgerCustomType = "lcoder/usage"

// usageEntry is one ledger record: the usage of one completed turn, keyed by
// the turn's assistant message id so a UI rebuilding history can look it up.
type usageEntry struct {
	MessageID string          `json:"message_id"`
	Turn      int             `json:"turn"`
	Usage     models.LLMUsage `json:"usage"`
}

// recordUsageEntry appends one turn's usage to the session ledger. It runs
// inside the same synchronous TurnEnd subscription as the message mirror
// (and therefore before the automatic checkpoint), so the ledger is never
// newer than the messages on disk. A failure is non-fatal — usage is display
// accounting, not conversation state — and follows the mirror's `_ =` style;
// a dropped entry shows up as a missing cost line, never as corruption.
func (c *Core) recordUsageEntry(sess *session.Session, ev events.TurnEndEvent) {
	data, err := json.Marshal(usageEntry{
		MessageID: ev.Message.ID,
		Turn:      ev.Base.Turn,
		Usage:     ev.Usage,
	})
	if err != nil {
		return
	}
	_ = sess.AppendCustomEntry(usageLedgerCustomType, data)
}

// usageEntries reads back the active branch's ledger, skipping entries that
// fail to decode (a hand-edited or future-versioned file must not break the
// aggregate).
func usageEntries(sess *session.Session) []usageEntry {
	var out []usageEntry
	for _, e := range sess.CustomEntries(usageLedgerCustomType) {
		var entry usageEntry
		if err := json.Unmarshal(e.Data, &entry); err != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// UsageSummary aggregates the active session's usage ledger. Read-only: it
// recomputes from the session's custom entries, so no state needs to survive
// a crash or a session switch.
func (c *Core) UsageSummary() agentapi.UsageSummary {
	sess := c.mirror.activeSession()
	if sess == nil {
		return agentapi.UsageSummary{}
	}
	var sum agentapi.UsageSummary
	for _, e := range usageEntries(sess) {
		sum.Turns++
		sum.TotalCost += e.Usage.TotalCost
		sum.PromptTokens += e.Usage.PromptTokens
		sum.CompletionTokens += e.Usage.CompletionTokens
		sum.CacheReadTokens += e.Usage.CacheReadTokens
		sum.CacheWriteTokens += e.Usage.CacheWriteTokens
	}
	return sum
}

// UsageLedger returns the active session's recorded usage keyed by assistant
// message id. A message whose turn was recorded more than once (should not
// happen; ids are unique per append) keeps the latest entry.
func (c *Core) UsageLedger() map[string]models.LLMUsage {
	sess := c.mirror.activeSession()
	if sess == nil {
		return nil
	}
	var out map[string]models.LLMUsage
	for _, e := range usageEntries(sess) {
		if e.MessageID == "" {
			continue
		}
		if out == nil {
			out = make(map[string]models.LLMUsage)
		}
		out[e.MessageID] = e.Usage
	}
	return out
}
