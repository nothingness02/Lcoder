package observability

import (
	"context"
	"sync"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestCollectorBasicFlow(t *testing.T) {
	exporter := NewMemoryExporter()
	collector := NewCollector(exporter)
	bus := events.New()
	defer bus.Close()
	collector.Subscribe(bus)

	ctx := context.Background()
	_ = bus.Emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: 0}})
	_ = bus.Emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: 0}})
	_ = bus.Emit(ctx, events.ToolExecutionStartEvent{Base: events.Base{Type: events.ToolExecutionStart, Turn: 0}, ToolCallID: "call_1", ToolName: "ls"})
	_ = bus.Emit(ctx, events.ToolExecutionEndEvent{Base: events.Base{Type: events.ToolExecutionEnd, Turn: 0}, ToolCallID: "call_1", ToolName: "ls"})
	_ = bus.Emit(ctx, events.TurnEndEvent{Base: events.Base{Type: events.TurnEnd, Turn: 0}})
	_ = bus.Emit(ctx, events.AgentEndEvent{Base: events.Base{Type: events.AgentEnd, Turn: 0}})

	_ = bus.Close()

	if len(exporter.Records) == 0 {
		t.Fatal("expected records")
	}

	var spanStarts, spanEnds, metrics int
	for _, r := range exporter.Records {
		switch r.Type {
		case "span_start", "span_end":
			if r.Type == "span_start" {
				spanStarts++
			} else {
				spanEnds++
			}
		case "metric":
			metrics++
		}
	}
	if spanStarts != 0 {
		t.Fatalf("expected no span_start records, got %d", spanStarts)
	}
	if spanEnds == 0 {
		t.Fatalf("expected span_end records")
	}
	if metrics == 0 {
		t.Fatal("expected metrics")
	}

	// All exported spans should carry a duration (0 is acceptable for sub-ms tests).
	for _, r := range exporter.Records {
		if r.Type == "span_end" && r.Span != nil {
			if r.Span.DurationMs < 0 {
				t.Fatalf("expected non-negative DurationMs for span %s, got %d", r.Span.Name, r.Span.DurationMs)
			}
		}
	}
}

func TestCollectorEmitsNoSpanStartRecords(t *testing.T) {
	exporter := NewMemoryExporter()
	collector := NewCollector(exporter)
	bus := events.New()
	defer bus.Close()
	collector.Subscribe(bus)

	ctx := context.Background()
	_ = bus.Emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: 0}})
	_ = bus.Emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: 0}})
	_ = bus.Emit(ctx, events.MessageStartEvent{Base: events.Base{Type: events.MessageStart, Turn: 0}, Message: models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: "hi"})})
	_ = bus.Emit(ctx, events.MessageEndEvent{Base: events.Base{Type: events.MessageEnd, Turn: 0}})
	_ = bus.Emit(ctx, events.ToolExecutionStartEvent{Base: events.Base{Type: events.ToolExecutionStart, Turn: 0}, ToolCallID: "call_1", ToolName: "ls"})
	_ = bus.Emit(ctx, events.ToolExecutionEndEvent{Base: events.Base{Type: events.ToolExecutionEnd, Turn: 0}, ToolCallID: "call_1", ToolName: "ls"})
	_ = bus.Emit(ctx, events.TurnEndEvent{Base: events.Base{Type: events.TurnEnd, Turn: 0}})
	_ = bus.Emit(ctx, events.AgentEndEvent{Base: events.Base{Type: events.AgentEnd, Turn: 0}})

	_ = bus.Close()

	for _, r := range exporter.Records {
		if r.Type == "span_start" {
			t.Fatalf("unexpected span_start record: %+v", r)
		}
	}
}

type memoryAuditLogger struct {
	mu      sync.Mutex
	Records []AuditRecord
}

func (m *memoryAuditLogger) Log(record AuditRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Records = append(m.Records, record)
	return nil
}

func (m *memoryAuditLogger) Close() error { return nil }

func TestCollectorAuditEvent(t *testing.T) {
	exporter := NewMemoryExporter()
	logger := &memoryAuditLogger{}
	collector := NewCollectorWithAudit(exporter, "sess-1", logger)
	bus := events.New()
	defer bus.Close()
	collector.Subscribe(bus)

	ctx := context.Background()
	_ = bus.Emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: 0}})
	_ = bus.Emit(ctx, events.AuditEvent{
		Base:       events.Base{Type: events.Audit, Turn: 0},
		ToolCallID: "call_1",
		ToolName:   "write",
		Args:       map[string]any{"path": "foo.go"},
		Decision:   "allow",
		Allowed:    true,
	})

	_ = bus.Close()

	if len(logger.Records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(logger.Records))
	}
	rec := logger.Records[0]
	if rec.SessionID != "sess-1" || rec.ToolName != "write" || !rec.Allowed {
		t.Fatalf("unexpected audit record: %+v", rec)
	}
}

func TestCollectorToolExecutionAudit(t *testing.T) {
	exporter := NewMemoryExporter()
	logger := &memoryAuditLogger{}
	collector := NewCollectorWithAudit(exporter, "sess-2", logger)
	bus := events.New()
	defer bus.Close()
	collector.Subscribe(bus)

	ctx := context.Background()
	_ = bus.Emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: 0}})
	_ = bus.Emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: 0}})
	_ = bus.Emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: 0},
		ToolCallID: "call_2",
		ToolName:   "bash",
		Args:       map[string]any{"command": "go test ./..."},
	})
	_ = bus.Emit(ctx, events.ToolExecutionEndEvent{
		Base:       events.Base{Type: events.ToolExecutionEnd, Turn: 0},
		ToolCallID: "call_2",
		ToolName:   "bash",
		Result:     models.NewToolExecutionResultText("ok"),
	})

	_ = bus.Close()

	if len(logger.Records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(logger.Records))
	}
	rec := logger.Records[0]
	if rec.ToolName != "bash" || rec.Decision != "allow" || !rec.Allowed {
		t.Fatalf("unexpected audit record: %+v", rec)
	}
	if rec.Args["command"] != "go test ./..." {
		t.Fatalf("expected args preserved, got %+v", rec.Args)
	}
}

func TestRecordTTFT(t *testing.T) {
	exporter := NewMemoryExporter()
	collector := NewCollector(exporter)
	_ = collector.RecordTTFT(3, 250)

	var found bool
	for _, r := range exporter.Records {
		if r.Type == "metric" && r.Metric != nil && r.Metric.Name == "llm_ttft_ms" {
			found = true
			if r.Metric.Value != 250 {
				t.Fatalf("expected ttft 250, got %f", r.Metric.Value)
			}
		}
	}
	if !found {
		t.Fatal("expected llm_ttft_ms metric")
	}
}

func TestRecordLLMUsage(t *testing.T) {
	exporter := NewMemoryExporter()
	collector := NewCollector(exporter)
	if err := collector.RecordLLMUsage(models.LLMUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		TotalCost:        0.002,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	if len(exporter.Records) != 6 {
		t.Fatalf("expected 6 usage records, got %d", len(exporter.Records))
	}
}
