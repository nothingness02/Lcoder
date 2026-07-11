package observability

import (
	"bytes"
	"fmt"
	"time"

	"github.com/lcoder/lcoder/internal/fsutil"
)

// MarkdownExporter renders a session report as Markdown.
type MarkdownExporter struct {
	records []Record
}

// NewMarkdownExporter creates a Markdown report builder.
func NewMarkdownExporter() *MarkdownExporter {
	return &MarkdownExporter{}
}

// Export accumulates a record for the final report.
func (m *MarkdownExporter) Export(record Record) error {
	m.records = append(m.records, record)
	return nil
}

// Close is a no-op for MarkdownExporter.
func (m *MarkdownExporter) Close() error { return nil }

// Save writes the Markdown report to disk.
func (m *MarkdownExporter) Save(path string) error {
	return fsutil.WritePrivateFile(path, []byte(m.Render()))
}

// Render returns the Markdown report string.
func (m *MarkdownExporter) Render() string {
	stats := ComputeStats(m.records)
	var b bytes.Buffer

	b.WriteString("# Lcoder Session Report\n\n")
	b.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().Format(time.RFC3339)))

	b.WriteString("## Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| --- | --- |\n")
	b.WriteString(fmt.Sprintf("| Turns | %d |\n", stats.Turns))
	b.WriteString(fmt.Sprintf("| LLM Calls | %d |\n", stats.LLMCalls))
	b.WriteString(fmt.Sprintf("| Tool Calls | %d |\n", stats.ToolCalls))
	b.WriteString(fmt.Sprintf("| Tool Errors | %d |\n", stats.ToolErrors))
	b.WriteString(fmt.Sprintf("| Input Tokens | %d |\n", stats.InputTokens))
	b.WriteString(fmt.Sprintf("| Output Tokens | %d |\n", stats.OutputTokens))
	b.WriteString(fmt.Sprintf("| Total Tokens | %d |\n", stats.TotalTokens))
	b.WriteString(fmt.Sprintf("| Estimated Cost | $%.6f |\n", stats.TotalCost))
	b.WriteString(fmt.Sprintf("| Duration | %d ms |\n", stats.TotalDurationMs))

	b.WriteString("\n## Trace\n\n")
	b.WriteString("| Time | Name | Duration | Status |\n")
	b.WriteString("| --- | --- | --- | --- |\n")

	for _, r := range m.records {
		if r.Type != "span_end" || r.Span == nil {
			continue
		}
		duration := ""
		if r.Span.DurationMs > 0 {
			duration = fmt.Sprintf("%d ms", r.Span.DurationMs)
		}
		b.WriteString(fmt.Sprintf(
			"| %s | %s | %s | %s |\n",
			time.UnixMilli(r.Span.StartTime).Format("15:04:05"),
			r.Span.Name,
			duration,
			r.Span.Status,
		))
	}

	return b.String()
}
