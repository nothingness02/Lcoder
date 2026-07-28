package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestParseSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"only summary", "<summary>kept text</summary>", "kept text"},
		{"analysis discarded", "<analysis>scratch</analysis>\n<summary>final</summary>", "final"},
		{"no tags falls back", "plain reply", "plain reply"},
		{"unclosed summary", "<summary>tail only", "tail only"},
		{"whitespace trimmed", "<summary>\n  body  \n</summary>", "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSummary(tc.in); got != tc.want {
				t.Fatalf("parseSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLLMSummarizerExtractsSummary(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("<analysis>noise</analysis><summary>did the thing</summary>"), nil),
	))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})
	out, err := summarize(context.Background(), []models.AgentMessage{models.UserMessage("hello")}, "")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if out != "did the thing" {
		t.Fatalf("expected extracted summary, got %q", out)
	}
}

func TestLLMSummarizerEmptyMessages(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("unused"), nil)))
	summarize := NewLLMSummarizer(client, models.ModelRef{})
	out, err := summarize(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("expected no error for empty input, got %v", err)
	}
	if out == "" {
		t.Fatal("expected a non-empty placeholder summary")
	}
}

func TestLLMSummarizerNilClient(t *testing.T) {
	summarize := NewLLMSummarizer(nil, models.ModelRef{})
	if _, err := summarize(context.Background(), []models.AgentMessage{models.UserMessage("x")}, ""); err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestLLMSummarizerStreamError(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.ErrorEvent("internal", "boom")))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})
	if _, err := summarize(context.Background(), []models.AgentMessage{models.UserMessage("x")}, ""); err == nil {
		t.Fatal("expected error on stream error event")
	}
}

func TestLLMSummarizerSendsSerializedTruncatedInput(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("<summary>ok</summary>"), nil),
	))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})

	big := strings.Repeat("x", 50000)
	msgs := []models.AgentMessage{
		models.UserMessage("do the thing"),
		models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
			ToolCallID: "c1", Name: "bash",
			Content: []models.ContentPart{models.TextContent{Text: big}},
		}),
	}
	if _, err := summarize(context.Background(), msgs, ""); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	req := adapter.LastRequest()
	if len(req.Messages) != 1 || req.Messages[0].Role != models.RoleUser {
		t.Fatalf("expected a single synthetic user message, got %+v", req.Messages)
	}
	text := req.Messages[0].Text()
	if !strings.Contains(text, "[User]: do the thing") {
		t.Fatalf("input not serialized: %q", text[:min(200, len(text))])
	}
	if !strings.Contains(text, "truncated") {
		t.Fatal("tool result not truncated")
	}
	if len(text) > summaryMaxInputChars {
		t.Fatalf("serialized input %d chars exceeds cap %d", len(text), summaryMaxInputChars)
	}
}

func TestLLMSummarizerCapsWholeInput(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("<summary>ok</summary>"), nil),
	))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})

	// User messages bypass the per-tool-result cap, so ~30 messages of 2000
	// chars each push the serialized input well past summaryMaxInputChars.
	msgs := make([]models.AgentMessage, 0, 30)
	for i := 0; i < 30; i++ {
		msgs = append(msgs, models.UserMessage(strings.Repeat("u", 2000)))
	}
	if _, err := summarize(context.Background(), msgs, ""); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	req := adapter.LastRequest()
	if len(req.Messages) != 1 || req.Messages[0].Role != models.RoleUser {
		t.Fatalf("expected a single synthetic user message, got %+v", req.Messages)
	}
	text := req.Messages[0].Text()
	if !strings.Contains(text, "[input truncated]") {
		t.Fatal("whole-input truncation marker missing")
	}
	if len(text) > summaryMaxInputChars {
		t.Fatalf("serialized input %d chars exceeds hard cap %d", len(text), summaryMaxInputChars)
	}
}

// Repeated compaction must carry the earlier summary forward in the request, and
// must label it so the model merges into it rather than compressing it again.
func TestLLMSummarizerSendsPriorSummary(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("<summary>merged</summary>"), nil),
	))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})

	out, err := summarize(context.Background(),
		[]models.AgentMessage{models.UserMessage("new work")}, "EARLIER SUMMARY")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if out != "merged" {
		t.Fatalf("expected merged summary, got %q", out)
	}

	text := adapter.LastRequest().Messages[0].Text()
	if !strings.Contains(text, "EARLIER SUMMARY") {
		t.Fatal("prior summary missing from the request")
	}
	if !strings.Contains(text, "<previous_summary>") || !strings.Contains(text, "<new_messages>") {
		t.Fatalf("prior and new material must be labelled separately, got %q", text)
	}
	if !strings.Contains(text, "new work") {
		t.Fatal("new messages missing from the request")
	}
}

// A fold with nothing new to summarize must not discard the accumulated summary.
func TestLLMSummarizerEmptyMessagesKeepsPrior(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("unused"), nil)))
	summarize := NewLLMSummarizer(client, models.ModelRef{})

	out, err := summarize(context.Background(), nil, "EARLIER SUMMARY")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if out != "EARLIER SUMMARY" {
		t.Fatalf("prior summary must survive an empty fold, got %q", out)
	}
}

// The prior summary has its own char budget so a long transcript cannot squeeze
// out the only surviving record of everything folded earlier.
func TestLLMSummarizerCapsPriorSeparately(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Done(models.AssistantMessage("<summary>ok</summary>"), nil),
	))
	summarize := NewLLMSummarizer(client, models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"})

	prior := strings.Repeat("p", summaryPriorChars*2)
	msgs := make([]models.AgentMessage, 0, 30)
	for i := 0; i < 30; i++ {
		msgs = append(msgs, models.UserMessage(strings.Repeat("u", 2000)))
	}
	if _, err := summarize(context.Background(), msgs, prior); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	text := adapter.LastRequest().Messages[0].Text()
	if !strings.Contains(text, "<previous_summary>") {
		t.Fatal("prior summary was dropped entirely under transcript pressure")
	}
	if !strings.Contains(text, "[input truncated]") {
		t.Fatal("expected truncation markers")
	}
	// The two budgets have to compose: independently capping each one let the
	// combined request run 25% past the hard cap.
	if len(text) > summaryMaxInputChars {
		t.Fatalf("combined input %d chars exceeds hard cap %d", len(text), summaryMaxInputChars)
	}
	// The prior must actually get its reserved slice, not a token remnant.
	if got := strings.Index(text, "</previous_summary>") - len(priorOpenTag); got < summaryPriorChars/2 {
		t.Fatalf("prior summary slice %d chars is far below its %d reservation", got, summaryPriorChars)
	}
}
