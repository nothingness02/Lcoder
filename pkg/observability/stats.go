package observability

import (
	"fmt"
	"strings"
)

// SessionStats aggregates observability records into human-readable stats.
type SessionStats struct {
	Turns           int
	LLMCalls        int
	ToolCalls       int
	ToolErrors      int
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	TotalCost       float64
	TotalDurationMs int64
}

// ComputeStats aggregates records into stats.
func ComputeStats(records []Record) SessionStats {
	var s SessionStats
	var agentStart, agentEnd int64
	seenToolCalls := make(map[string]bool)

	for _, r := range records {
		switch r.Type {
		case "span_end":
			if r.Span != nil {
				if r.Span.Name == "agent_run" {
					agentStart = r.Span.StartTime
					agentEnd = r.Span.EndTime
				}
				if r.Span.Name == "llm_response" {
					s.LLMCalls++
				}
				if _, ok := seenToolCalls[r.Span.SpanID]; !ok {
					seenToolCalls[r.Span.SpanID] = true
					if strings.HasPrefix(r.Span.Name, "tool_") {
						s.ToolCalls++
						if r.Span.Status == SpanError {
							s.ToolErrors++
						}
					}
				}
			}
		case "metric":
			if r.Metric == nil {
				continue
			}
			switch r.Metric.Name {
			case "llm_total_tokens":
				s.TotalTokens += int(r.Metric.Value)
			case "llm_prompt_tokens":
				s.InputTokens += int(r.Metric.Value)
			case "llm_completion_tokens":
				s.OutputTokens += int(r.Metric.Value)
			case "llm_cost_usd":
				s.TotalCost += r.Metric.Value
			case "agent_turn_duration_ms":
				s.Turns++
			}
		}
	}

	if agentEnd > agentStart {
		s.TotalDurationMs = agentEnd - agentStart
	}
	return s
}

// String formats stats as text.
func (s SessionStats) String() string {
	return fmt.Sprintf(
		"Turns: %d\nLLM calls: %d\nTool calls: %d (errors: %d)\nTotal tokens: %d\nEstimated cost: $%.6f\nDuration: %dms",
		s.Turns, s.LLMCalls, s.ToolCalls, s.ToolErrors, s.TotalTokens, s.TotalCost, s.TotalDurationMs,
	)
}
