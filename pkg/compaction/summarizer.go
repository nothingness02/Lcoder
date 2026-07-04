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

// summaryTimeout bounds a single summarization call. SummarizeFunc has no
// context parameter, so the deadline is applied internally.
const summaryTimeout = 90 * time.Second

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
</summary>`

// NewLLMSummarizer returns a SummarizeFunc that asks the LLM engine to compact
// older messages into a dual-stage summary, keeping only the <summary> block.
// The returned function matches contextmgr.SummarizeFunc without importing it.
func NewLLMSummarizer(client *llm.Client, model models.ModelRef) SummarizeFunc {
	return func(messages []models.AgentMessage) (string, error) {
		if client == nil {
			return "", fmt.Errorf("llm summarizer: nil client")
		}
		if len(messages) == 0 {
			return "No earlier messages.", nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), summaryTimeout)
		defer cancel()

		req := models.TurnRequest{
			Model:        model,
			SystemPrompt: summaryInstruction,
			Messages:     messages,
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
