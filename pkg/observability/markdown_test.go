package observability

import (
	"os"
	"strings"
	"testing"
)

func TestMarkdownExporterRender(t *testing.T) {
	exporter := NewMarkdownExporter()
	records := []Record{
		{Type: "span_end", Span: &Span{SpanID: "root", Name: "agent_run", StartTime: 0, EndTime: 1000, DurationMs: 1000, Status: SpanOK}},
		{Type: "span_end", Span: &Span{SpanID: "llm1", Name: "llm_response", ParentID: "turn1", StartTime: 100, EndTime: 400, DurationMs: 300, Status: SpanOK}},
		{Type: "span_end", Span: &Span{SpanID: "tool1", Name: "tool_ls", ParentID: "turn1", StartTime: 500, EndTime: 550, DurationMs: 50, Status: SpanOK}},
		{Type: "metric", Metric: &Metric{Name: "llm_total_tokens", Value: 150}},
		{Type: "metric", Metric: &Metric{Name: "llm_cost_usd", Value: 0.002}},
	}
	for _, r := range records {
		if err := exporter.Export(r); err != nil {
			t.Fatalf("export: %v", err)
		}
	}

	rendered := exporter.Render()
	if !strings.Contains(rendered, "# Lcoder Session Report") {
		t.Fatal("missing report title")
	}
	if !strings.Contains(rendered, "LLM Calls") {
		t.Fatal("missing LLM Calls summary")
	}
	if !strings.Contains(rendered, "Tool Calls") {
		t.Fatal("missing Tool Calls summary")
	}
	if !strings.Contains(rendered, "agent_run") {
		t.Fatal("missing agent_run trace")
	}
	if !strings.Contains(rendered, "llm_response") {
		t.Fatal("missing llm_response trace")
	}
	if !strings.Contains(rendered, "tool_ls") {
		t.Fatal("missing tool_ls trace")
	}
}

func TestMarkdownExporterSave(t *testing.T) {
	exporter := NewMarkdownExporter()
	_ = exporter.Export(Record{Type: "span_end", Span: &Span{Name: "agent_run", StartTime: 0, EndTime: 100, DurationMs: 100, Status: SpanOK}})

	dir := t.TempDir()
	path := dir + "/report.md"
	if err := exporter.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved report: %v", err)
	}
	if !strings.Contains(string(data), "agent_run") {
		t.Fatal("saved report missing content")
	}
}
