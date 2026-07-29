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
// runtime changes (currently the sampling rate and context snapshot toggle) to
// a SamplingExporter and ContextSnapshotRecorder.
type ConfigWatcher struct {
	path     string
	sampler  *SamplingExporter
	recorder *ContextSnapshotRecorder

	mu       sync.Mutex
	watcher  *fsnotify.Watcher
	done     chan struct{}
	debounce *time.Timer
}

// NewConfigWatcher creates a watcher for path that updates sampler and recorder
// when the file changes.
func NewConfigWatcher(path string, sampler *SamplingExporter, recorder *ContextSnapshotRecorder) *ConfigWatcher {
	return &ConfigWatcher{
		path:     path,
		sampler:  sampler,
		recorder: recorder,
		done:     make(chan struct{}),
	}
}

// NewConfigWatcherFromConfig creates a watcher only when runtime sampling is
// enabled. It returns nil, nil when sampling is disabled so callers can treat
// it as optional.
func NewConfigWatcherFromConfig(path string, cfg config.ObservabilityConfig, sampler *SamplingExporter, recorder *ContextSnapshotRecorder) (*ConfigWatcher, error) {
	if !cfg.Sampling.Enabled {
		return nil, nil
	}
	return NewConfigWatcher(path, sampler, recorder), nil
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

// Close stops the watcher and waits for the event loop to exit.
func (w *ConfigWatcher) Close() error {
	w.mu.Lock()
	watcher := w.watcher
	w.watcher = nil
	w.mu.Unlock()

	// Close the fsnotify watcher first: this shuts down the Events/Errors
	// channels, which causes loop() to exit its select and return.
	if watcher != nil {
		_ = watcher.Close()
	}
	// Drain the done channel so a double-close does not panic, then signal
	// the loop (belt-and-suspenders — the closed watcher already signals it).
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	return nil
}

func (w *ConfigWatcher) loop() {
	const debounceDelay = 100 * time.Millisecond

	// Snapshot the watcher under the lock so we don't race with Close()
	// setting w.watcher = nil.
	w.mu.Lock()
	watcher := w.watcher
	w.mu.Unlock()
	if watcher == nil {
		return
	}

	for {
		select {
		case ev, ok := <-watcher.Events:
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

		case err, ok := <-watcher.Errors:
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
	if w.recorder != nil {
		w.recorder.SetEnabled(cfg.ContextSnapshots.Enabled)
	}
}
