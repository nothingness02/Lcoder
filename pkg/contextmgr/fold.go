package contextmgr

import (
	"context"
	"errors"

	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/models"
)

// defaultKeepRecentTokens mirrors pi's keepRecentTokens default.
const defaultKeepRecentTokens = 20000

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

	if cut <= 0 {
		return 0, false
	}
	return cut, split
}

// FoldResult describes a committed (or degraded) fold.
type FoldResult struct {
	Committed    bool
	Summary      string // committed summary text; empty when Degraded
	FirstKeptID  string // ID of the first kept message (cut boundary)
	TokensBefore int    // estimated total prompt tokens before the fold
	Degraded     bool   // true when the breaker is open: truncated without summary
	SplitTurn    bool   // true when the cut landed inside the last turn
}

// foldOlder folds messages [0:cut] of the recent block into a summary and
// commits [summary, tail...] in place. The cut point comes from findCutPoint
// with a per-level token budget. Split turns summarize history and turn
// prefix separately and merge them. A circuit-breaker-open summarizer
// (ErrCompactionSkipped) degrades to truncation without summary so context
// pressure is still relieved. State is untouched on any other error.
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

	summaryText, err := m.summarizeForFold(ctx, msgs, cut, split)
	if err != nil {
		if errors.Is(err, compaction.ErrCompactionSkipped) {
			// Degraded: drop the older span without a summary.
			m.ReplaceRecent(append([]models.AgentMessage(nil), msgs[cut:]...))
			res.Degraded = true
			return res, nil
		}
		return FoldResult{}, err
	}

	res.Summary = "[Summary of earlier conversation]\n\n" + summaryText
	summary := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: res.Summary}).
		WithMetadata("compacted", true)
	m.ReplaceRecent(append([]models.AgentMessage{summary}, msgs[cut:]...))
	return res, nil
}

// summarizeForFold produces the summary body for the folded span. Split turns
// summarize the pre-turn history and the in-turn prefix separately.
func (m *Manager) summarizeForFold(ctx context.Context, msgs []models.AgentMessage, cut int, split bool) (string, error) {
	if !split {
		return m.summarizer(ctx, msgs[:cut])
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
		s, err := m.summarizer(ctx, msgs[:turnStart])
		if err != nil {
			return "", err
		}
		histSummary = s
	}
	prefixSummary, err := m.summarizer(ctx, msgs[turnStart:cut])
	if err != nil {
		return "", err
	}
	if histSummary == "" {
		return "[Summary of current turn so far]\n" + prefixSummary, nil
	}
	return histSummary + "\n\n[Summary of current turn so far]\n" + prefixSummary, nil
}
