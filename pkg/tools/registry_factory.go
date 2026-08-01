package tools

import (
	"fmt"
	"sync"
)

// DefaultFactories is the global factory registry for built-in tools.
// Custom extension packages can register factories in their init() functions.
var DefaultFactories = &FactoryRegistry{}

// FactoryRegistry is a thread-safe registry of tool factories.
type FactoryRegistry struct {
	factories map[string]Factory
	mu        sync.RWMutex
}

// Register adds a factory under the given tool name.
func (r *FactoryRegistry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories == nil {
		r.factories = make(map[string]Factory)
	}
	r.factories[name] = factory
}

// Create instantiates a tool for the given working directory.
func (r *FactoryRegistry) Create(name, cwd string) (Executable, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[name]
	if !ok {
		return nil, false
	}
	return factory(cwd), true
}

// Unregister removes a factory (used by tests and extension teardown).
func (r *FactoryRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factories, name)
}

// Names returns the registered tool names.
func (r *FactoryRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// RegisterBuiltinFactories registers all factories from DefaultFactories into the given Registry.
func (r *Registry) RegisterBuiltinFactories(cwd string) error {
	for _, name := range DefaultFactories.Names() {
		exec, ok := DefaultFactories.Create(name, cwd)
		if !ok {
			return fmt.Errorf("factory disappeared for %q", name)
		}
		r.Register(name, exec)
	}
	return nil
}
