package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadObservabilityConfigMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	cfg, err := LoadObservabilityConfig(path)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.Exporter.Type != "file" {
		t.Errorf("default exporter type: got %q, want file", cfg.Exporter.Type)
	}
	if cfg.Audit.Enabled != true {
		t.Errorf("audit should be enabled by default")
	}
	if cfg.Sampling.Enabled != true {
		t.Errorf("sampling should be enabled by default")
	}
	if cfg.Sampling.Rate != 1.0 {
		t.Errorf("default sampling rate: got %v, want 1.0", cfg.Sampling.Rate)
	}
}

func TestLoadObservabilityConfigParsesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	content := `
exporter:
  type: sqlite
  output: /tmp/obs.db
  options:
    flush_interval_ms: 500
audit:
  enabled: false
  path: /tmp/audit.jsonl
sampling:
  enabled: true
  rate: 0.25
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadObservabilityConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Exporter.Type != "sqlite" {
		t.Errorf("exporter type: got %q, want sqlite", cfg.Exporter.Type)
	}
	if cfg.Exporter.Output != "/tmp/obs.db" {
		t.Errorf("exporter output: got %q, want /tmp/obs.db", cfg.Exporter.Output)
	}
	if got := cfg.Exporter.Options["flush_interval_ms"]; got != 500 {
		t.Errorf("exporter option: got %v, want 500", got)
	}
	if cfg.Audit.Enabled != false {
		t.Errorf("audit enabled: got %v, want false", cfg.Audit.Enabled)
	}
	if cfg.Audit.Path != "/tmp/audit.jsonl" {
		t.Errorf("audit path: got %q, want /tmp/audit.jsonl", cfg.Audit.Path)
	}
	if cfg.Sampling.Rate != 0.25 {
		t.Errorf("sampling rate: got %v, want 0.25", cfg.Sampling.Rate)
	}
}

func TestLoadObservabilityConfigInvalidYAMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling: rate: not_a_number"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadObservabilityConfig(path); err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestDefaultObservabilityPath(t *testing.T) {
	path := DefaultObservabilityPath()
	if !filepath.IsAbs(path) {
		t.Errorf("default path should be absolute: %s", path)
	}
	if filepath.Base(path) != "observability.yaml" {
		t.Errorf("default path base: got %s, want observability.yaml", filepath.Base(path))
	}
}
