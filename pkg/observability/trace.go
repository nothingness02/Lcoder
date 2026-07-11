package observability

import (
	"fmt"
	"strings"
)

// TraceFormatter renders a simple text trace from observability records.
type TraceFormatter struct{}

// NewTraceFormatter creates a trace formatter.
func NewTraceFormatter() *TraceFormatter {
	return &TraceFormatter{}
}

// Render returns a human-readable trace.
func (f *TraceFormatter) Render(records []Record) string {
	var spans []*Span
	for _, r := range records {
		if r.Type == "span_end" && r.Span != nil {
			spans = append(spans, r.Span)
		}
	}

	children := make(map[string][]*Span)
	var roots []*Span
	for _, s := range spans {
		if s.ParentID == "" {
			roots = append(roots, s)
		} else {
			children[s.ParentID] = append(children[s.ParentID], s)
		}
	}

	var b strings.Builder
	var render func(s *Span, depth int)
	render = func(s *Span, depth int) {
		prefix := strings.Repeat("  ", depth)
		duration := ""
		if s.DurationMs > 0 {
			duration = fmt.Sprintf(" %dms", s.DurationMs)
		}
		b.WriteString(fmt.Sprintf("%s- %s [%s]%s\n", prefix, s.Name, s.Status, duration))
		for _, c := range children[s.SpanID] {
			render(c, depth+1)
		}
	}
	for _, r := range roots {
		render(r, 0)
	}

	for _, r := range records {
		if r.Type != "metric" || r.Metric == nil {
			continue
		}
		labels := ""
		if len(r.Metric.Labels) > 0 {
			labels = " " + formatLabels(r.Metric.Labels)
		}
		b.WriteString(fmt.Sprintf("  metric %s = %g%s\n", r.Metric.Name, r.Metric.Value, labels))
	}
	return b.String()
}

// FormatTrace reads observability records from a file and renders a trace.
func FormatTrace(path string) (string, error) {
	records, err := LoadFile(path)
	if err != nil {
		return "", err
	}
	formatter := NewTraceFormatter()
	return formatter.Render(records), nil
}
