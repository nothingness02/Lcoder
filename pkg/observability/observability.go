package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

// SpanStatus indicates the outcome of a span.
type SpanStatus string

const (
	SpanOK       SpanStatus = "ok"
	SpanError    SpanStatus = "error"
	SpanCanceled SpanStatus = "canceled"
)

// Span represents a timed operation in the agent.
type Span struct {
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Name       string         `json:"name"`
	StartTime  int64          `json:"start_time"`
	EndTime    int64          `json:"end_time,omitempty"`
	Status     SpanStatus     `json:"status"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Events     []SpanEvent    `json:"events,omitempty"`
}

// SpanEvent is a point-in-time event attached to a span.
type SpanEvent struct {
	Timestamp int64          `json:"timestamp"`
	Name      string         `json:"name"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// Metric is a numeric measurement.
type Metric struct {
	Timestamp int64             `json:"timestamp"`
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// Record is a union type written to the observability log.
type Record struct {
	Type string `json:"type"`
	*Span
	*Metric
}

func (r Record) MarshalJSON() ([]byte, error) {
	switch r.Type {
	case "span_start", "span_end":
		return json.Marshal(&struct {
			Type string `json:"type"`
			*Span
		}{Type: r.Type, Span: r.Span})
	case "metric":
		return json.Marshal(&struct {
			Type string `json:"type"`
			*Metric
		}{Type: r.Type, Metric: r.Metric})
	default:
		return json.Marshal(r)
	}
}

// Exporter writes observability records.
type Exporter interface {
	Export(record Record) error
	Close() error
}

// Collector subscribes to agent events and builds spans/metrics/audit logs.
type Collector struct {
	exporter    Exporter
	auditLogger AuditLogger
	sessionID   string

	mu        sync.Mutex
	rootSpan  *Span
	turnSpans map[int]*Span
	llmSpans  map[string]*Span
	toolSpans map[string]*Span

	turnStartTimes map[int]int64
	llmStartTimes  map[string]int64
	toolStartTimes map[string]int64
	toolArgs       map[string]map[string]any
}

// NewCollector creates an observability collector.
func NewCollector(exporter Exporter) *Collector {
	return NewCollectorWithAudit(exporter, "", nil)
}

// NewCollectorWithAudit creates an observability collector with optional audit logging.
func NewCollectorWithAudit(exporter Exporter, sessionID string, auditLogger AuditLogger) *Collector {
	return &Collector{
		exporter:       exporter,
		auditLogger:    auditLogger,
		sessionID:      sessionID,
		turnSpans:      make(map[int]*Span),
		llmSpans:       make(map[string]*Span),
		toolSpans:      make(map[string]*Span),
		turnStartTimes: make(map[int]int64),
		llmStartTimes:  make(map[string]int64),
		toolStartTimes: make(map[string]int64),
		toolArgs:       make(map[string]map[string]any),
	}
}

// Subscribe registers the collector on an event bus.
func (c *Collector) Subscribe(bus *events.Bus) func() {
	return bus.Subscribe(c.handle)
}

func (c *Collector) handle(ctx context.Context, ev events.Event) error {
	now := time.Now().UnixMilli()
	c.mu.Lock()
	defer c.mu.Unlock()

	switch e := ev.(type) {
	case events.AgentStartEvent:
		c.rootSpan = &Span{
			TraceID:   uuid.New().String(),
			SpanID:    uuid.New().String()[:8],
			Name:      "agent_run",
			StartTime: now,
			Status:    SpanOK,
		}
		_ = c.exporter.Export(Record{Type: "span_start", Span: c.rootSpan})

	case events.AgentEndEvent:
		if c.rootSpan == nil {
			return nil
		}
		c.rootSpan.EndTime = now
		_ = c.exporter.Export(Record{Type: "span_end", Span: c.rootSpan})

	case events.TurnStartEvent:
		if c.rootSpan == nil {
			return nil
		}
		c.turnStartTimes[e.Turn] = now
		span := &Span{
			TraceID:   c.rootSpan.TraceID,
			SpanID:    uuid.New().String()[:8],
			ParentID:  c.rootSpan.SpanID,
			Name:      fmt.Sprintf("turn_%d", e.Turn),
			StartTime: now,
			Status:    SpanOK,
		}
		c.turnSpans[e.Turn] = span
		_ = c.exporter.Export(Record{Type: "span_start", Span: span})

	case events.TurnEndEvent:
		span, ok := c.turnSpans[e.Turn]
		if !ok {
			return nil
		}
		span.EndTime = now
		_ = c.exporter.Export(Record{Type: "span_end", Span: span})

		// Emit turn duration metric.
		start := c.turnStartTimes[e.Turn]
		_ = c.exporter.Export(Record{
			Type: "metric",
			Metric: &Metric{
				Timestamp: now,
				Name:      "agent_turn_duration_ms",
				Value:     float64(now - start),
				Labels:    map[string]string{"turn": fmt.Sprintf("%d", e.Turn)},
			},
		})

	case events.MessageStartEvent:
		if c.rootSpan == nil {
			return nil
		}
		spanID := uuid.New().String()[:8]
		c.llmStartTimes[spanID] = now
		span := &Span{
			TraceID:   c.rootSpan.TraceID,
			SpanID:    spanID,
			ParentID:  c.turnSpans[e.Turn].SpanID,
			Name:      "llm_response",
			StartTime: now,
			Status:    SpanOK,
			Attributes: map[string]any{
				"role": e.Message.Role,
			},
		}
		c.llmSpans[spanID] = span

	case events.MessageEndEvent:
		// Close the most recent open LLM span for this turn.
		var target *Span
		for _, span := range c.llmSpans {
			if span.EndTime == 0 {
				target = span
			}
		}
		if target == nil {
			return nil
		}
		target.EndTime = now
		_ = c.exporter.Export(Record{Type: "span_end", Span: target})

		start := c.llmStartTimes[target.SpanID]
		_ = c.exporter.Export(Record{
			Type: "metric",
			Metric: &Metric{
				Timestamp: now,
				Name:      "llm_response_duration_ms",
				Value:     float64(now - start),
				Labels: map[string]string{
					"provider": safeString(c.rootSpan.Attributes, "provider"),
					"model":    safeString(c.rootSpan.Attributes, "model"),
				},
			},
		})

	case events.ToolExecutionStartEvent:
		if c.rootSpan == nil {
			return nil
		}
		c.toolStartTimes[e.ToolCallID] = now
		if e.Args != nil {
			c.toolArgs[e.ToolCallID] = e.Args
		}
		span := &Span{
			TraceID:   c.rootSpan.TraceID,
			SpanID:    e.ToolCallID,
			ParentID:  c.turnSpans[e.Turn].SpanID,
			Name:      fmt.Sprintf("tool_%s", e.ToolName),
			StartTime: now,
			Status:    SpanOK,
			Attributes: map[string]any{
				"tool_name": e.ToolName,
			},
		}
		c.toolSpans[e.ToolCallID] = span
		_ = c.exporter.Export(Record{Type: "span_start", Span: span})

	case events.ToolExecutionEndEvent:
		span, ok := c.toolSpans[e.ToolCallID]
		if !ok {
			return nil
		}
		span.EndTime = now
		if e.IsError {
			span.Status = SpanError
		}
		_ = c.exporter.Export(Record{Type: "span_end", Span: span})

		start := c.toolStartTimes[e.ToolCallID]
		duration := now - start
		status := "success"
		if e.IsError {
			status = "error"
		}
		_ = c.exporter.Export(Record{
			Type: "metric",
			Metric: &Metric{
				Timestamp: now,
				Name:      "tool_execution_duration_ms",
				Value:     float64(duration),
				Labels: map[string]string{
					"tool":   e.ToolName,
					"status": status,
				},
			},
		})

		if c.auditLogger != nil {
			errorText := ""
			if e.IsError {
				errorText = toolResultErrorText(e.Result)
			}
			_ = c.auditLogger.Log(AuditRecord{
				Timestamp:  now,
				SessionID:  c.sessionID,
				Turn:       e.Turn,
				ToolCallID: e.ToolCallID,
				ToolName:   e.ToolName,
				Args:       c.toolArgs[e.ToolCallID],
				Decision:   "allow",
				Allowed:    true,
				Blocked:    false,
				IsError:    e.IsError,
				Error:      errorText,
				DurationMs: duration,
			})
		}

	case events.ErrorEvent:
		if c.rootSpan != nil {
			c.rootSpan.Status = SpanError
			c.rootSpan.Events = append(c.rootSpan.Events, SpanEvent{
				Timestamp: now,
				Name:      "error",
				Payload:   map[string]any{"message": e.Message},
			})
		}

	case events.AuditEvent:
		if c.auditLogger != nil {
			_ = c.auditLogger.Log(AuditRecord{
				Timestamp:   now,
				SessionID:   c.sessionID,
				Turn:        e.Turn,
				ToolCallID:  e.ToolCallID,
				ToolName:    e.ToolName,
				Args:        e.Args,
				Decision:    e.Decision,
				Allowed:     e.Allowed,
				Blocked:     e.Blocked,
				BlockReason: e.BlockReason,
			})
		}
	}
	return nil
}

// RecordRuntimeError records a non-fatal runtime error against the active run span.
func (c *Collector) RecordRuntimeError(message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rootSpan == nil {
		return nil
	}
	c.rootSpan.Status = SpanError
	c.rootSpan.Events = append(c.rootSpan.Events, SpanEvent{
		Timestamp: time.Now().UnixMilli(),
		Name:      "runtime_error",
		Payload:   map[string]any{"message": message},
	})
	return nil
}

// Close releases resources held by the collector and exporter.
func (c *Collector) Close() error {
	var errs []error
	if c.exporter != nil {
		if err := c.exporter.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if c.auditLogger != nil {
		if err := c.auditLogger.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// RecordTTFT records the time-to-first-token for a provider turn.
func (c *Collector) RecordTTFT(turn int, durationMs int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	provider, model := "", ""
	if c.rootSpan != nil {
		provider = safeString(c.rootSpan.Attributes, "provider")
		model = safeString(c.rootSpan.Attributes, "model")
	}
	return c.exporter.Export(Record{
		Type: "metric",
		Metric: &Metric{
			Timestamp: time.Now().UnixMilli(),
			Name:      "llm_ttft_ms",
			Value:     float64(durationMs),
			Labels: map[string]string{
				"turn":     fmt.Sprintf("%d", turn),
				"provider": provider,
				"model":    model,
			},
		},
	})
}

// RecordLLMUsage records LLM token usage and cost after a turn completes.
func (c *Collector) RecordLLMUsage(usage models.LLMUsage) error {
	c.mu.Lock()
	var provider, model string
	if c.rootSpan != nil {
		provider = safeString(c.rootSpan.Attributes, "provider")
		model = safeString(c.rootSpan.Attributes, "model")
	}
	c.mu.Unlock()

	now := time.Now().UnixMilli()
	labels := map[string]string{
		"provider": provider,
		"model":    model,
	}
	records := []Record{
		{Type: "metric", Metric: &Metric{Timestamp: now, Name: "llm_prompt_tokens", Value: float64(usage.PromptTokens), Labels: labels}},
		{Type: "metric", Metric: &Metric{Timestamp: now, Name: "llm_completion_tokens", Value: float64(usage.CompletionTokens), Labels: labels}},
		{Type: "metric", Metric: &Metric{Timestamp: now, Name: "llm_total_tokens", Value: float64(usage.TotalTokens), Labels: labels}},
		{Type: "metric", Metric: &Metric{Timestamp: now, Name: "llm_cache_read_tokens", Value: float64(usage.CacheReadTokens), Labels: labels}},
		{Type: "metric", Metric: &Metric{Timestamp: now, Name: "llm_cache_write_tokens", Value: float64(usage.CacheWriteTokens), Labels: labels}},
		{Type: "metric", Metric: &Metric{Timestamp: now, Name: "llm_cost_usd", Value: usage.TotalCost, Labels: labels}},
	}
	for _, r := range records {
		if err := c.exporter.Export(r); err != nil {
			return err
		}
	}
	return nil
}

func safeString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key].(string)
	if !ok {
		return ""
	}
	return v
}

func toolResultErrorText(result models.ToolExecutionResult) string {
	out := result.Text()
	if len(out) > 500 {
		out = out[:497] + "..."
	}
	return out
}
