package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lcoder/lcoder/pkg/config"
)

// ConfigWatcher monitors the observability configuration file and applies
// runtime changes (currently the sampling rate) to a SamplingExporter.
type ConfigWatcher struct {
	path    string
	sampler *SamplingExporter

	mu       sync.Mutex
	watcher  *fsnotify.Watcher
	done     chan struct{}
	debounce *time.Timer
}

// NewConfigWatcher creates a watcher for path that updates sampler when the
// file changes.
func NewConfigWatcher(path string, sampler *SamplingExporter) *ConfigWatcher {
	return &ConfigWatcher{
		path:    path,
		sampler: sampler,
		done:    make(chan struct{}),
	}
}

// NewConfigWatcherFromConfig creates a watcher only when runtime sampling is
// enabled. It returns nil, nil when sampling is disabled so callers can treat
// it as optional.
func NewConfigWatcherFromConfig(path string, cfg config.ObservabilityConfig, sampler *SamplingExporter) (*ConfigWatcher, error) {
	if !cfg.Sampling.Enabled {
		return nil, nil
	}
	return NewConfigWatcher(path, sampler), nil
}

// Start begins watching the configuration file for changes.
func (w *ConfigWatcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watcher != nil {
		return fmt.Errorf("watcher already started")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}

	// Watch both the file and its parent directory so renames/replacements are
	// detected reliably across editors.
	if err := watcher.Add(w.path); err != nil {
		if !os.IsNotExist(err) {
			watcher.Close()
			return fmt.Errorf("watch file: %w", err)
		}
	}
	dir := filepath.Dir(w.path)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return fmt.Errorf("watch directory: %w", err)
	}

	w.watcher = watcher
	go w.loop()
	return nil
}

// Close stops the watcher.
func (w *ConfigWatcher) Close() error {
	close(w.done)
	w.mu.Lock()
	watcher := w.watcher
	w.watcher = nil
	w.mu.Unlock()
	if watcher != nil {
		return watcher.Close()
	}
	return nil
}

func (w *ConfigWatcher) loop() {
	const debounceDelay = 100 * time.Millisecond

	for {
		select {
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if ev.Name != w.path {
				continue
			}
			w.mu.Lock()
			if w.debounce != nil {
				w.debounce.Stop()
			}
			w.debounce = time.AfterFunc(debounceDelay, func() {
				w.reload()
			})
			w.mu.Unlock()

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "observability config watcher error: %v\n", err)

		case <-w.done:
			return
		}
	}
}

func (w *ConfigWatcher) reload() {
	cfg, err := config.LoadObservabilityConfig(w.path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: reload observability config: %v\n", err)
		return
	}
	if w.sampler != nil {
		w.sampler.SetRate(cfg.Sampling.Rate)
	}
}
