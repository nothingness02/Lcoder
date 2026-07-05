package observability

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
)

func TestNewExporterFromConfigUsesSampling(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "obs.jsonl")
	cfg := config.ObservabilityConfig{
		Exporter: config.ExporterConfig{
			Type:   "file",
			Output: output,
		},
		Sampling: config.SamplingConfig{
			Enabled: true,
			Rate:    0,
		},
	}

	ex, sampler, err := NewExporterFromConfig(cfg, "sess-1")
	if err != nil {
		t.Fatalf("create exporter: %v", err)
	}
	if sampler == nil {
		t.Fatal("expected sampling exporter")
	}
	defer ex.Close()

	if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 1}}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := os.Stat(output); err == nil {
		data, _ := os.ReadFile(output)
		if len(data) > 0 {
			t.Fatal("rate 0 should drop record")
		}
	}

	sampler.SetRate(1)
	if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 2}}); err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("rate 1 should write record")
	}
}

func TestNewExporterFromConfigSamplingDisabled(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "obs.jsonl")
	cfg := config.ObservabilityConfig{
		Exporter: config.ExporterConfig{Type: "file", Output: output},
		Sampling: config.SamplingConfig{Enabled: false},
	}

	ex, sampler, err := NewExporterFromConfig(cfg, "sess-2")
	if err != nil {
		t.Fatalf("create exporter: %v", err)
	}
	if sampler != nil {
		t.Fatal("sampling disabled should not return sampler")
	}
	defer ex.Close()

	if _, ok := ex.(*FileExporter); !ok {
		t.Fatalf("expected *FileExporter, got %T", ex)
	}
}

func TestNewExporterFromConfigUnknownType(t *testing.T) {
	cfg := config.ObservabilityConfig{
		Exporter: config.ExporterConfig{Type: "not-real"},
	}
	if _, _, err := NewExporterFromConfig(cfg, "sess"); err == nil {
		t.Fatal("expected error for unknown exporter type")
	}
}

func TestNewAuditLoggerFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	cfg := config.ObservabilityConfig{
		Audit: config.AuditConfig{Enabled: true, Path: path},
	}
	logger, err := NewAuditLoggerFromConfig(cfg, "sess-3")
	if err != nil {
		t.Fatalf("create audit logger: %v", err)
	}
	if logger == nil {
		t.Fatal("expected audit logger")
	}
	defer logger.Close()

	if err := logger.Log(AuditRecord{ToolName: "test"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("audit record should be written")
	}
}

func TestNewAuditLoggerFromConfigDisabled(t *testing.T) {
	cfg := config.ObservabilityConfig{
		Audit: config.AuditConfig{Enabled: false},
	}
	logger, err := NewAuditLoggerFromConfig(cfg, "sess-4")
	if err != nil {
		t.Fatalf("create audit logger: %v", err)
	}
	if logger != nil {
		t.Fatal("disabled audit should return nil logger")
	}
}
