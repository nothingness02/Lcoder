package observability

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
)

func TestConfigWatcherUpdatesSamplingRate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 0\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	inner := NewMemoryExporter()
	sampler := NewSamplingExporter(inner, 0)
	defer sampler.Close()

	w := NewConfigWatcher(path, sampler, nil)
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
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 1\n"), 0o600); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Poll until the new rate is applied or a generous timeout elapses.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sampler.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 2}})
		if len(inner.Records) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected one record after rate update, got %d", len(inner.Records))
}

func TestConfigWatcherNoSamplerStillStarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 0.5\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	w := NewConfigWatcher(path, nil, nil)
	if err := w.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer w.Close()

	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 1\n"), 0o644); err != nil {
		t.Fatalf("update config: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
}

func TestConfigWatcherTogglesContextSnapshots(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observability.yaml")
	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 1\ncontext_snapshots:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	inner := NewMemoryExporter()
	sampler := NewSamplingExporter(inner, 1)
	defer sampler.Close()

	snapDir := filepath.Join(dir, "snapshots")
	recorder := NewContextSnapshotRecorder("sess-cw", config.ContextSnapshotsConfig{Enabled: false, OutputDir: snapDir})

	w, err := NewConfigWatcherFromConfig(path, config.ObservabilityConfig{Sampling: config.SamplingConfig{Enabled: true, Rate: 1}}, sampler, recorder)
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer w.Close()

	state := &contextmgr.ManagerState{Blocks: []contextmgr.BlockState{}}
	_ = recorder.Record(state, "end", 0)
	if _, err := os.ReadFile(filepath.Join(snapDir, "context-turn-0-end.md")); err == nil {
		t.Fatal("expected no snapshot when initially disabled")
	}

	if err := os.WriteFile(path, []byte("sampling:\n  enabled: true\n  rate: 1\ncontext_snapshots:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("update config: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = recorder.Record(state, "end", 0)
		if _, err := os.ReadFile(filepath.Join(snapDir, "context-turn-0-end.md")); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected snapshot after enabling via config watcher")
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

	w, err := NewConfigWatcherFromConfig(path, config.ObservabilityConfig{Sampling: config.SamplingConfig{Enabled: true, Rate: 0.5}}, sampler, nil)
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	defer w.Close()

	if w == nil {
		t.Fatal("expected watcher")
	}
}
