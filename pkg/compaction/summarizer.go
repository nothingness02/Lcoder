package compaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// summaryTimeout bounds a single summarization call. The deadline is applied on
// top of the caller's context, whichever fires first.
const summaryTimeout = 90 * time.Second

// SummarizeFunc generates a summary from a slice of messages.
// In production this calls the LLM engine. The context carries the agent's
// run cancellation so abort/Ctrl+C interrupts in-flight summarization.
//
// prior is the previous compaction's summary, or empty on the first fold. It is
// passed separately rather than left among messages so repeated compactions
// carry the earlier summary forward verbatim instead of summarizing it again:
// a summary of a summary loses detail on every pass, and the original task
// statement is what degrades first. It matches contextmgr.SummarizeFunc
// without importing it (contextmgr imports this package, so the reverse
// would be a cycle).
type SummarizeFunc func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error)

// summaryToolResultChars caps one tool result inside the serialized input.
const summaryToolResultChars = 2000

// summaryMaxInputChars caps the whole serialized input (~12k tokens at 4
// chars/token), keeping the summarization request far below any model window.
const summaryMaxInputChars = 48000

// summaryPriorChars reserves part of summaryMaxInputChars for the
// carried-forward summary, so a long transcript can never squeeze out the
// accumulated summary — that is the only copy of everything folded in earlier
// passes. The transcript takes the remainder, keeping the combined input inside
// the whole-input cap.
const summaryPriorChars = 12000

// summaryInputMinTranscriptChars floors the transcript slice so a pathologically
// long prior summary still leaves room for the new messages being folded.
const summaryInputMinTranscriptChars = 4000

// Tags labelling the two halves of an iterative summarization input.
const (
	priorOpenTag  = "<previous_summary>\n"
	priorCloseTag = "\n</previous_summary>\n\n"
	newOpenTag    = "<new_messages>\n"
	newCloseTag   = "\n</new_messages>"
)

// summaryInputTruncatedSuffix marks that the serialized input was cut to fit
// the whole-input cap.
const summaryInputTruncatedSuffix = "\n...[input truncated]"

// summaryInstruction is the dual-stage system prompt. The model first drafts an
// <analysis> block (scratch reasoning, discarded) and then emits a <summary>
// block, which is the only part injected back into the live context.
const summaryInstruction = `You are compacting an earlier portion of a coding conversation so it can be replaced by a concise summary while work continues.

Produce your output in exactly two stages:

<analysis>
Think through the conversation: what was being built, which decisions were made, what changed, what is still open. This block is scratch space and will be DISCARDED.
</analysis>

<summary>
Write the durable summary that REPLACES the earlier messages. Be specific and concrete. Cover, in order, only the sections that apply:
1. Goal & intent — what the user is ultimately trying to accomplish.
2. Key decisions — choices made and the reasoning behind them.
3. File changes — files created/modified and what changed in each.
4. Code & APIs — important function names, signatures, and types involved.
5. Errors & fixes — failures encountered and how they were resolved.
6. Current state — what is done and verified vs. in progress.
7. Open questions — unresolved issues or pending decisions.
8. Next steps — the immediate next actions.
9. User preferences — explicit constraints or style the user asked for.
Preserve exact identifiers (paths, symbols, flags). Do not invent facts. Omit empty sections.
</summary>

When the input contains a <previous_summary> block, it is the summary of an EARLIER compaction of this same conversation, and <new_messages> holds only what happened since. Your output must be a single merged summary covering both, written as one continuous account rather than two halves. Facts in the previous summary are already condensed and cannot be recovered from anywhere else: carry them forward, and drop one only when the new messages show it is now wrong or resolved. The original goal and the user's stated constraints must survive every pass verbatim — they are the first thing lost to repeated summarization and the most expensive to lose.`

// NewLLMSummarizer returns a SummarizeFunc that asks the LLM engine to compact
// older messages into a dual-stage summary, keeping only the <summary> block.
func NewLLMSummarizer(client *llm.Client, model models.ModelRef) SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error) {
		if client == nil {
			return "", fmt.Errorf("llm summarizer: nil client")
		}
		if len(messages) == 0 {
			// A repeated fold can legitimately have nothing new to summarize. The
			// prior summary must still survive: returning a placeholder here would
			// discard everything the earlier folds established.
			if strings.TrimSpace(prior) != "" {
				return prior, nil
			}
			return "No earlier messages.", nil
		}

		ctx, cancel := context.WithTimeout(ctx, summaryTimeout)
		defer cancel()

		// The prior summary gets a reserved slice of the input budget and the
		// transcript gets the rest, so a long transcript cannot squeeze out the only
		// surviving record of earlier folds — and the two together still respect the
		// whole-input cap.
		p := strings.TrimSpace(prior)
		if len(p) > summaryPriorChars {
			p = p[:summaryPriorChars-len(summaryInputTruncatedSuffix)] + summaryInputTruncatedSuffix
		}
		wrapper := 0
		if p != "" {
			wrapper = len(priorOpenTag) + len(priorCloseTag) + len(newOpenTag) + len(newCloseTag) + len(p)
		}
		transcriptCap := summaryMaxInputChars - wrapper
		if transcriptCap < summaryInputMinTranscriptChars {
			transcriptCap = summaryInputMinTranscriptChars
		}

		serialized := SerializeConversation(messages, summaryToolResultChars)
		if len(serialized) > transcriptCap {
			serialized = serialized[:transcriptCap-len(summaryInputTruncatedSuffix)] + summaryInputTruncatedSuffix
		}

		userText := serialized
		if p != "" {
			userText = priorOpenTag + p + priorCloseTag + newOpenTag + serialized + newCloseTag
		}

		req := models.TurnRequest{
			Model:        model,
			SystemPrompt: summaryInstruction,
			Messages: []models.AgentMessage{
				models.NewAgentMessage(models.RoleUser, models.TextContent{Text: userText}),
			},
		}

		stream, err := client.StreamTurn(ctx, req)
		if err != nil {
			return "", fmt.Errorf("llm summarizer: stream turn: %w", err)
		}

		var final models.AgentMessage
		var gotFinal bool
	loop:
		for {
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("llm summarizer: read stream: %w", ctx.Err())
			case ev, ok := <-stream:
				if !ok {
					break loop
				}
				switch ev.Kind {
				case provider.KindDone:
					final = ev.Message
					gotFinal = true
				case provider.KindError:
					if ev.Err != nil {
						return "", fmt.Errorf("llm summarizer: engine error: %w", ev.Err)
					}
					return "", fmt.Errorf("llm summarizer: engine error")
				}
			}
		}

		if !gotFinal {
			return "", fmt.Errorf("llm summarizer: stream ended without a summary")
		}
		summary := parseSummary(final.Text())
		if strings.TrimSpace(summary) == "" {
			return "", fmt.Errorf("llm summarizer: empty summary")
		}
		return summary, nil
	}
}

// parseSummary extracts the content of the <summary>...</summary> block,
// discarding any <analysis> scratch reasoning. When no summary tag is present
// it falls back to the whole text so a well-formed-but-untagged reply is kept.
func parseSummary(text string) string {
	const open, close = "<summary>", "</summary>"
	start := strings.Index(text, open)
	if start < 0 {
		return strings.TrimSpace(text)
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return strings.TrimSpace(text[start:])
	}
	return strings.TrimSpace(text[start : start+end])
}
