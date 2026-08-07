package host

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// turnUsage builds a provider usage report for llmtest.Done. The engine
// recomputes the cost fields from its pricing table, so tests assert the
// token fields exactly and cross-check the cost against the ledger entries.
func turnUsage(prompt, completion int) *models.LLMUsage {
	return &models.LLMUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      prompt + completion,
	}
}

// readUsageEntries decodes the ledger entries straight from the session —
// the same source UsageSummary/UsageLedger aggregate.
func readUsageEntries(t *testing.T, sess *session.Session) []usageEntry {
	t.Helper()
	var out []usageEntry
	for _, e := range sess.CustomEntries(usageLedgerCustomType) {
		var entry usageEntry
		if err := json.Unmarshal(e.Data, &entry); err != nil {
			t.Fatalf("ledger entry does not decode: %v", err)
		}
		out = append(out, entry)
	}
	return out
}

// A completed turn appends one lcoder/usage custom entry carrying the turn's
// assistant message id and the provider usage, in the same synchronous
// subscription that mirrors the messages.
func TestUsageLedgerRecordedOnTurnEnd(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	reply := textMsg(models.RoleAssistant, "turn reply")
	client := llmtest.Client(llmtest.Turn(llmtest.Done(reply, turnUsage(100, 40))))
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	if err := core.Prompt(context.Background(), models.UserMessage("hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	entries := readUsageEntries(t, sess)
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}
	if entries[0].MessageID != reply.ID {
		t.Fatalf("entry message id = %q, want the assistant message %q", entries[0].MessageID, reply.ID)
	}
	if entries[0].Turn != 0 {
		t.Fatalf("entry turn = %d, want the run loop's first turn (0)", entries[0].Turn)
	}
	if entries[0].Usage.PromptTokens != 100 || entries[0].Usage.CompletionTokens != 40 {
		t.Fatalf("entry usage = %+v, want 100/40 tokens", entries[0].Usage)
	}

	// The ledger is durable: a session reloaded from disk decodes the same
	// entry (metadata comes back as generic any, so this also covers the
	// re-marshal read path).
	reloaded, err := store.LoadByID(testCWD, sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if got := readUsageEntries(t, reloaded); len(got) != 1 || got[0].MessageID != reply.ID {
		t.Fatalf("reloaded ledger = %+v, want the one entry for %q", got, reply.ID)
	}
}

// UsageSummary aggregates the active branch's ledger: turns, token buckets,
// and cost all sum over the entries.
func TestUsageSummaryAggregatesLedger(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "one"), turnUsage(100, 40))),
		llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "two"), turnUsage(200, 60))),
	)
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	for _, text := range []string{"first", "second"} {
		if err := core.Prompt(context.Background(), models.UserMessage(text)); err != nil {
			t.Fatalf("prompt %q: %v", text, err)
		}
	}

	sum := core.UsageSummary()
	if sum.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", sum.Turns)
	}
	if sum.PromptTokens != 300 || sum.CompletionTokens != 100 {
		t.Fatalf("tokens = %d/%d, want 300/100", sum.PromptTokens, sum.CompletionTokens)
	}
	// The engine owns the actual pricing; the summary must equal the sum of
	// whatever the ledger recorded.
	var wantCost float64
	for _, e := range readUsageEntries(t, sess) {
		wantCost += e.Usage.TotalCost
	}
	if sum.TotalCost != wantCost {
		t.Fatalf("TotalCost = %v, want the ledger sum %v", sum.TotalCost, wantCost)
	}
}

// The ledger follows branch semantics: after /retry's fork, the abandoned
// turn's entry stays on the old branch and the aggregate counts only the
// replacement turn.
func TestUsageLedgerFollowsRetryBranch(t *testing.T) {
	store := newStore(t)
	sess := mustCreate(t, store)
	first := textMsg(models.RoleAssistant, "first answer")
	second := textMsg(models.RoleAssistant, "second answer")
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(first, turnUsage(100, 40))),
		llmtest.Turn(llmtest.Done(second, turnUsage(200, 60))),
	)
	core, _ := newTestCore(t, client, nil, sess, store, nil, nil)
	defer core.Close()

	if err := core.Prompt(context.Background(), models.UserMessage("q")); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if sum := core.UsageSummary(); sum.Turns != 1 || sum.PromptTokens != 100 {
		t.Fatalf("summary after turn 1 = %+v, want 1 turn/100 prompt tokens", sum)
	}

	// /retry of the only prompt forks at the root; the re-run records a new
	// entry on the new branch.
	if err := core.TruncateAfter(""); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if sum := core.UsageSummary(); sum.Turns != 0 {
		t.Fatalf("summary after fork = %+v, want empty (abandoned turn is off-branch)", sum)
	}
	if err := core.Prompt(context.Background(), models.UserMessage("q")); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}

	sum := core.UsageSummary()
	if sum.Turns != 1 || sum.PromptTokens != 200 {
		t.Fatalf("summary after retry = %+v, want 1 turn/200 prompt tokens (only the replacement turn)", sum)
	}
	ledger := core.UsageLedger()
	if len(ledger) != 1 {
		t.Fatalf("ledger = %d entries, want 1", len(ledger))
	}
	if _, ok := ledger[second.ID]; !ok {
		t.Fatalf("ledger must key the replacement message %q, got %v", second.ID, ledger)
	}
	if _, ok := ledger[first.ID]; ok {
		t.Fatal("ledger must not contain the abandoned turn's message")
	}
}

// The aggregate is a property of the session: switching sessions re-reads the
// target session's ledger (from disk), and switching back restores it.
func TestUsageSummaryFollowsSessionSwitch(t *testing.T) {
	store := newStore(t)
	sess0 := mustCreate(t, store)
	sess1 := mustCreate(t, store)
	// Create defers the file write until the first message, so sess1 needs one
	// append to be loadable by OpenSession.
	if err := sess1.Append(models.UserMessage("older question")); err != nil {
		t.Fatal(err)
	}
	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg(models.RoleAssistant, "reply"), turnUsage(100, 40))))
	core, _ := newTestCore(t, client, nil, sess0, store, nil, nil)
	defer core.Close()

	if err := core.Prompt(context.Background(), models.UserMessage("hi")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if sum := core.UsageSummary(); sum.Turns != 1 {
		t.Fatalf("summary in sess0 = %+v, want 1 turn", sum)
	}

	if err := core.OpenSession(sess1.ID); err != nil {
		t.Fatalf("open sess1: %v", err)
	}
	if sum := core.UsageSummary(); sum != (agentapi.UsageSummary{}) {
		t.Fatalf("summary in empty sess1 = %+v, want zero", sum)
	}
	if got := core.UsageLedger(); len(got) != 0 {
		t.Fatalf("ledger in empty sess1 = %v, want empty", got)
	}

	if err := core.OpenSession(sess0.ID); err != nil {
		t.Fatalf("reopen sess0: %v", err)
	}
	sum := core.UsageSummary()
	if sum.Turns != 1 || sum.PromptTokens != 100 {
		t.Fatalf("summary after reopening sess0 = %+v, want 1 turn/100 prompt tokens", sum)
	}
	if got := len(core.UsageLedger()); got != 1 {
		t.Fatalf("ledger after reopening sess0 = %d entries, want 1", got)
	}
}
