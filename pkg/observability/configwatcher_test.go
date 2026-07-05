package observability

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/config"
)

func TestConfigWatcherUpdatesSamplingRate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 0\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	inner := NewMemoryExporter()
	sampler := NewSamplingExporter(inner, 0)
	defer sampler.Close()

	w := NewConfigWatcher(path, sampler)
	if err := w.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer w.Close()

	// Initial rate is 0, so this record is dropped.
	if err := sampler.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 1}}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(inner.Records) != 0 {
		t.Fatal("initial rate 0 should drop record")
	}

	// Update the configuration file and wait for the watcher to reload it.
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 1\n"), 0o644); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Wait for fsnotify event + debounce + reload.
	time.Sleep(300 * time.Millisecond)

	if err := sampler.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 2}}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(inner.Records) != 1 {
		t.Fatalf("expected one record after rate update, got %d", len(inner.Records))
	}
}

func TestConfigWatcherNoSamplerStillStarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 0.5\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	w := NewConfigWatcher(path, nil)
	if err := w.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer w.Close()

	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 1\n"), 0o644); err != nil {
		t.Fatalf("update config: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
}

func TestNewConfigWatcherFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 0.5\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	inner := NewMemoryExporter()
	sampler := NewSamplingExporter(inner, 0.5)
	defer sampler.Close()

	w, err := NewConfigWatcherFromConfig(path, config.ObservabilityConfig{Sampling: config.SamplingConfig{Enabled: true, Rate: 0.5}}, sampler)
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	defer w.Close()

	if w == nil {
		t.Fatal("expected watcher")
	}
}
