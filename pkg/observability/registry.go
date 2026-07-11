package observability

import (
	"fmt"
	"sync"
)

// ExporterFactory creates an Exporter from a config map and output path.
type ExporterFactory func(cfg map[string]any, output string) (Exporter, error)

// Registry holds named exporter factories.
type Registry struct {
	factories map[string]ExporterFactory
	mu        sync.RWMutex
}

// NewRegistry creates an empty exporter registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]ExporterFactory)}
}

// Register adds an exporter factory under a name.
func (r *Registry) Register(name string, factory ExporterFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Create instantiates the named exporter.
func (r *Registry) Create(name string, cfg map[string]any, output string) (Exporter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("unknown exporter: %s", name)
	}
	return factory(cfg, output)
}

// Names returns registered exporter names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry returns a registry with all built-in exporters registered.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register("file", func(cfg map[string]any, output string) (Exporter, error) {
		path := output
		if path == "" {
			path = DefaultPath("")
		}
		return NewFileExporter(path)
	})
	r.Register("sqlite", func(cfg map[string]any, output string) (Exporter, error) {
		return NewSQLiteExporter(output)
	})
	r.Register("html", func(cfg map[string]any, output string) (Exporter, error) {
		return NewHTMLExporter(), nil
	})
	r.Register("markdown", func(cfg map[string]any, output string) (Exporter, error) {
		return NewMarkdownExporter(), nil
	})
	r.Register("prometheus", func(cfg map[string]any, output string) (Exporter, error) {
		return NewPrometheusExporter(), nil
	})
	return r
}
