package observability

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"time"
)

// HTMLExporter writes a single-session report to an HTML file.
type HTMLExporter struct {
	records []Record
}

// NewHTMLExporter creates an HTML report builder.
func NewHTMLExporter() *HTMLExporter {
	return &HTMLExporter{}
}

// AddRecord accumulates a record.
func (h *HTMLExporter) AddRecord(r Record) {
	h.records = append(h.records, r)
}

// Export appends a record to the HTML report.
func (h *HTMLExporter) Export(record Record) error {
	h.AddRecord(record)
	return nil
}

// Close is a no-op for HTMLExporter.
func (h *HTMLExporter) Close() error { return nil }

// Save writes the HTML report to disk.
func (h *HTMLExporter) Save(path string) error {
	if err := os.MkdirAll(pathParent(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(h.Render()), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func pathParent(path string) string {
	idx := 0
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			idx = i
			break
		}
	}
	if idx == 0 {
		return "."
	}
	return path[:idx]
}

// Render returns the HTML report string.
func (h *HTMLExporter) Render() string {
	stats := ComputeStats(h.records)
	var buf bytes.Buffer
	buf.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Lcoder Session Report</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 900px; margin: 2em auto; line-height: 1.6; }
h1, h2 { color: #333; }
table { border-collapse: collapse; width: 100%; margin-top: 1em; }
th, td { border: 1px solid #ddd; padding: 8px; text-align: left; }
th { background: #f4f4f4; }
.metric { font-size: 1.5em; font-weight: bold; color: #0066cc; }
</style>
</head>
<body>
<h1>Lcoder Session Report</h1>
<p>Generated: ` + time.Now().Format(time.RFC3339) + `</p>
<h2>Summary</h2>
<table>
<tr><th>Metric</th><th>Value</th></tr>
<tr><td>Turns</td><td>` + fmt.Sprint(stats.Turns) + `</td></tr>
<tr><td>LLM Calls</td><td>` + fmt.Sprint(stats.LLMCalls) + `</td></tr>
<tr><td>Tool Calls</td><td>` + fmt.Sprint(stats.ToolCalls) + `</td></tr>
<tr><td>Tool Errors</td><td>` + fmt.Sprint(stats.ToolErrors) + `</td></tr>
<tr><td>Total Tokens</td><td>` + fmt.Sprint(stats.TotalTokens) + `</td></tr>
<tr><td>Estimated Cost</td><td>$` + fmt.Sprintf("%.6f", stats.TotalCost) + `</td></tr>
<tr><td>Duration</td><td>` + fmt.Sprint(stats.TotalDurationMs) + ` ms</td></tr>
</table>
<h2>Trace</h2>
<table>
<tr><th>Time</th><th>Type</th><th>Name</th><th>Status</th></tr>
`)
	for _, r := range h.records {
		if r.Type != "span_start" || r.Span == nil {
			continue
		}
		duration := ""
		if r.Span.EndTime > 0 {
			duration = fmt.Sprintf("%d ms", r.Span.EndTime-r.Span.StartTime)
		}
		buf.WriteString(fmt.Sprintf(
			"<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			time.UnixMilli(r.Span.StartTime).Format("15:04:05"),
			html.EscapeString(r.Type),
			html.EscapeString(r.Span.Name),
			html.EscapeString(duration+" "+string(r.Span.Status)),
		))
	}
	buf.WriteString(`</table>
</body>
</html>`)
	return buf.String()
}
