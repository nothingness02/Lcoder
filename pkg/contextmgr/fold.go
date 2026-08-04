package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/models"
)

// defaultKeepRecentTokens mirrors pi's keepRecentTokens default.
const defaultKeepRecentTokens = 20000

// summaryDisplayPrefix labels a committed summary in the live context. It is
// stripped before the summary is carried into the next fold, so the label is not
// re-summarized or nested on each pass.
const summaryDisplayPrefix = "[Summary of earlier conversation]\n\n"

// findCutPoint returns the index at which to cut msgs: [0:cut] is folded,
// [cut:] is kept. It walks backward accumulating estimated tokens until the
// budget is exhausted, then adjusts to legal boundaries:
//
//   - never cut immediately before a tool_result (its tool_use would be
//     summarized away while the result survives as an orphan); the cut is
//     advanced past the leading tool_result run so the pair stays intact.
//   - the kept tail must include the last user message. When the budget cut
//     would land inside the final turn, the cut moves back to the turn start
//     unless allowSplit (reactive pressure) permits cutting mid-turn.
//   - a message-count floor (keepRecent) protects short/small conversations
//     from over-compaction; the cut that keeps MORE wins, except under
//     allowSplit where the token budget is authoritative.
//
// split=true means the cut lands inside the last turn (split turn).
func (m *Manager) findCutPoint(msgs []models.AgentMessage, tokenBudget int, allowSplit bool) (cut int, split bool) {
	n := len(msgs)
	if n == 0 {
		return 0, false
	}

	// Walk backward accumulating tokens.
	acc := 0
	cut = 0
	for i := n - 1; i >= 0; i-- {
		t := m.EstimateTokens(msgs[i : i+1])
		if acc+t > tokenBudget {
			cut = i + 1
			break
		}
		acc += t
	}
	if cut == 0 {
		return 0, false // everything fits; nothing to fold
	}

	// Legal boundary: never cut before a tool_result.
	for cut < n && msgs[cut].Role == models.RoleToolResult {
		cut++
	}
	if cut >= n {
		cut = n - 1 // degenerate tail; fold up to the last message
	}

	// Last user protection.
	lastUserIdx := -1
	for i := n - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx >= 0 && cut > lastUserIdx {
		if allowSplit {
			split = true
		} else {
			cut = lastUserIdx
		}
	}

	// Message-count floor (skipped when splitting: token budget is authoritative).
	if !split {
		floor := n - min(m.keepRecent, n)
		if m.keepRecent < 1 {
			floor = n - min(1, n)
		}
		if cut > floor {
			cut = floor
		}
		// The floor may re-land on a tool_result; re-adjust.
		for cut < n && msgs[cut].Role == models.RoleToolResult {
			cut++
		}
	}

	if cut >= n {
		return 0, false // empty kept tail: nothing worth folding
	}
	if cut <= 0 {
		return 0, false
	}
	return cut, split
}

// FoldResult describes a committed (or degraded) fold.
type FoldResult struct {
	Committed bool
	// Summary is the committed summary text. Never empty on a committed fold:
	// when Degraded it carries an explicit "summary unavailable" notice instead,
	// so the persisted compaction entry and the live context both say that the
	// dropped span has no summary rather than appearing to have a blank one.
	Summary      string
	FirstKeptID  string // ID of the first kept message (cut boundary)
	TokensBefore int    // estimated total prompt tokens before the fold
	Degraded     bool   // true when the breaker is open: truncated without a real summary
	SplitTurn    bool   // true when the cut landed inside the last turn
}

// foldOlder folds messages [0:cut] of the recent block into a summary and
// commits [summary, tail...] in place. The cut point comes from findCutPoint
// with a per-level token budget. Split turns summarize history and turn
// prefix separately and merge them. A circuit-breaker-open summarizer
// (ErrCompactionSkipped) degrades to truncation with an explicit
// summary-unavailable notice, so context pressure is relieved without leaving
// the model to read the surviving tail as the whole conversation. State is
// untouched on any other error.
func (m *Manager) foldOlder(ctx context.Context, level CompactionLevel) (FoldResult, error) {
	recent, ok := m.GetBlock(BlockRecent, "recent")
	if !ok || len(recent.Messages) == 0 {
		return FoldResult{}, nil
	}
	msgs := recent.Messages
	tokensBefore := m.currentTotalTokens()

	cut, split := m.findCutPoint(msgs, m.keepTokensForLevel(level), level == CompactionReactive)
	if cut == 0 {
		return FoldResult{}, nil
	}
	res := FoldResult{
		Committed:    true,
		FirstKeptID:  msgs[cut].ID,
		TokensBefore: tokensBefore,
		SplitTurn:    split,
	}

	// Locate the previous summary across the WHOLE block, not just the folded
	// span: the cut is chosen by token budget, so an existing summary can land in
	// the kept tail. Left there it would survive alongside the new summary — two
	// summaries in context, the stale one after the newer one — and the fold would
	// have run with prior empty, silently discarding everything it recorded.
	priorIdx, prior := findPriorSummary(msgs)
	keptTail := msgs[cut:]
	if priorIdx >= cut {
		keptTail = withoutIndex(keptTail, priorIdx-cut)
	}

	summaryText, err := m.summarizeForFold(ctx, msgs, cut, split, prior)
	if err != nil {
		if errors.Is(err, compaction.ErrCompactionSkipped) {
			// Degraded: drop the older span, but still say so in the context. An
			// empty summary here would reach the model as a blank system message
			// that structurally claims a summary exists while carrying nothing —
			// worse than no summary, because the model cannot tell the history was
			// truncated and reads the remaining tail as the whole conversation.
			res.Degraded = true
			res.Summary = degradedSummaryText(cut)
			notice := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: res.Summary}).
				WithMetadata("compacted", true)
			m.ReplaceRecent(append([]models.AgentMessage{notice}, keptTail...))
			// The recorded usage describes the pre-fold prompt; it is stale now.
			m.InvalidateRealUsage()
			// The fold rewrote the message indexes; a stale micro-compact cutoff
			// would point at the wrong content.
			m.resetMicroCompact()
			return res, m.recordFold(res)
		}
		return FoldResult{}, err
	}

	res.Summary = summaryDisplayPrefix + summaryText
	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: res.Summary}).
		WithMetadata("compacted", true)
	m.ReplaceRecent(append([]models.AgentMessage{summary}, keptTail...))
	// The recorded usage describes the pre-fold prompt; it is stale now.
	m.InvalidateRealUsage()
	// The fold rewrote the message indexes; a stale micro-compact cutoff
	// would point at the wrong content.
	m.resetMicroCompact()
	return res, m.recordFold(res)
}

// resetMicroCompact clears the micro-compact cutoff under the manager lock so
// it serializes with Detect/Apply in BuildTurnRequest and with status reads.
func (m *Manager) resetMicroCompact() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.microCompact != nil {
		m.microCompact.Reset(0)
	}
}

// recordFold hands a committed fold to the sink. It runs in the same call as the
// fold itself so a change to what a fold means cannot leave the durable record
// behind: both live here, one branch apart.
func (m *Manager) recordFold(res FoldResult) error {
	if m.sink == nil {
		return nil
	}
	if err := m.sink(res, m.AllMessages()); err != nil {
		return fmt.Errorf("record compaction: %w", err)
	}
	return nil
}

// degradedSummaryText is the placeholder committed when the summarizer is
// unavailable. It states what was lost and that it is unrecoverable from
// context, so the model asks or re-reads rather than assuming the surviving
// tail is the whole conversation.
func degradedSummaryText(dropped int) string {
	return fmt.Sprintf(
		"[Summary unavailable] The %d earliest messages of this conversation were "+
			"dropped to free context, and the summarizer was unavailable, so no summary "+
			"of them exists. Treat any earlier goal, decision, or file change as unknown "+
			"rather than absent: re-read files or ask before relying on that history.",
		dropped)
}

// findPriorSummary returns the index of the newest compaction summary in msgs
// and its text stripped of the display prefix, or (-1, "") when there is none.
func findPriorSummary(msgs []models.AgentMessage) (int, string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !isCompactedSummary(msgs[i]) {
			continue
		}
		return i, strings.TrimSpace(strings.TrimPrefix(msgs[i].Text(), summaryDisplayPrefix))
	}
	return -1, ""
}

// withoutIndex returns msgs with the element at idx removed. idx < 0 returns
// msgs unchanged.
func withoutIndex(msgs []models.AgentMessage, idx int) []models.AgentMessage {
	if idx < 0 || idx >= len(msgs) {
		return msgs
	}
	out := make([]models.AgentMessage, 0, len(msgs)-1)
	out = append(out, msgs[:idx]...)
	out = append(out, msgs[idx+1:]...)
	return out
}

// summarizeForFold produces the summary body for the folded span. Split turns
// summarize the pre-turn history and the in-turn prefix separately.
func (m *Manager) summarizeForFold(ctx context.Context, msgs []models.AgentMessage, cut int, split bool, prior string) (string, error) {
	// The prior summary is excluded from the transcript wherever it sits: passed
	// as prior it is merged forward, left inline it would be summarized again.
	priorIdx, _ := findPriorSummary(msgs)
	if !split {
		span := msgs[:cut]
		if priorIdx >= 0 && priorIdx < cut {
			span = withoutIndex(span, priorIdx)
		}
		return m.summarizer(ctx, span, prior)
	}
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			lastUserIdx = i
			break
		}
	}
	turnStart := lastUserIdx
	if turnStart < 0 {
		turnStart = 0
	}
	var histSummary string
	if turnStart > 0 {
		hist := msgs[:turnStart]
		if priorIdx >= 0 && priorIdx < turnStart {
			hist = withoutIndex(hist, priorIdx)
		}
		s, err := m.summarizer(ctx, hist, prior)
		if err != nil {
			return "", err
		}
		histSummary = s
	}
	prefixSummary, err := m.summarizer(ctx, msgs[turnStart:cut], "")
	if err != nil {
		return "", err
	}
	if histSummary == "" {
		return "[Summary of current turn so far]\n" + prefixSummary, nil
	}
	return histSummary + "\n\n[Summary of current turn so far]\n" + prefixSummary, nil
}
