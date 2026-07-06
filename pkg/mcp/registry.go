package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lcoder/lcoder/pkg/tools"
)

// ServerConfig describes an MCP server to connect to.
type ServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`            // "stdio" or "sse"
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Timeout   int               `json:"timeout"` // seconds; 0 -> 30
}

// Registry manages multiple MCP clients.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]*Client
	configs []ServerConfig
	errors  map[string]error
}

// NewRegistry creates an MCP registry from the provided configs.
func NewRegistry(configs []ServerConfig) *Registry {
	return &Registry{
		clients: make(map[string]*Client),
		configs: normalizeConfigs(configs),
		errors:  make(map[string]error),
	}
}

func normalizeConfigs(configs []ServerConfig) []ServerConfig {
	out := make([]ServerConfig, len(configs))
	for i, cfg := range configs {
		if cfg.Timeout <= 0 {
			cfg.Timeout = 30
		}
		out[i] = cfg
	}
	return out
}

// Connect starts all configured MCP servers.
func (r *Registry) Connect() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cfg := range r.configs {
		client, err := r.connectClient(cfg)
		if err != nil {
			r.errors[cfg.Name] = err
			continue
		}
		delete(r.errors, cfg.Name)
		r.clients[cfg.Name] = client
	}
	return nil
}

func (r *Registry) connectClient(cfg ServerConfig) (*Client, error) {
	transport, err := r.newTransport(cfg)
	if err != nil {
		return nil, err
	}
	client, err := NewClient(cfg.Name, transport)
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	return client, nil
}

func (r *Registry) newTransport(cfg ServerConfig) (Transport, error) {
	switch cfg.Transport {
	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp server %q: sse transport requires url", cfg.Name)
		}
		return NewSSETransport(cfg.URL, cfg.Headers, time.Duration(cfg.Timeout)*time.Second)
	case "streamable-http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp server %q: streamable-http transport requires url", cfg.Name)
		}
		return NewStreamableHTTPTransport(cfg.URL, cfg.Headers, time.Duration(cfg.Timeout)*time.Second)
	case "stdio":
		if len(cfg.Command) == 0 {
			return nil, fmt.Errorf("mcp server %q: stdio transport requires command", cfg.Name)
		}
		return NewStdioTransport(cfg.Command, cfg.Env)
	default:
		if cfg.Transport == "" {
			return nil, fmt.Errorf("mcp server %q: transport is required", cfg.Name)
		}
		return nil, fmt.Errorf("mcp server %q: unknown transport %q", cfg.Name, cfg.Transport)
	}
}

// Close shuts down all MCP clients.
func (r *Registry) Close() error {
	r.mu.Lock()
	clients := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		clients = append(clients, c)
	}
	r.clients = make(map[string]*Client)
	r.mu.Unlock()

	var firstErr error
	for _, c := range clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CloseServer disconnects a single configured MCP server.
func (r *Registry) CloseServer(name string) error {
	r.mu.Lock()
	client, ok := r.clients[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("mcp server %q not connected", name)
	}
	delete(r.clients, name)
	r.mu.Unlock()
	return client.Close()
}

// Reconnect closes and reopens a single configured MCP server.
// The existing Client pointer is preserved when already connected so that
// registered tool executables remain valid.
func (r *Registry) Reconnect(name string) error {
	r.mu.Lock()
	cfg, ok := r.findConfig(name)
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("mcp server %q not configured", name)
	}
	client, connected := r.clients[name]
	r.mu.Unlock()

	if !connected {
		newClient, err := r.connectClient(cfg)
		if err != nil {
			r.mu.Lock()
			r.errors[name] = err
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		delete(r.errors, name)
		r.clients[name] = newClient
		r.mu.Unlock()
		return nil
	}

	transport, err := r.newTransport(cfg)
	if err != nil {
		r.mu.Lock()
		r.errors[name] = err
		r.mu.Unlock()
		return err
	}
	if err := client.ReplaceTransport(context.Background(), transport); err != nil {
		_ = transport.Close()
		r.mu.Lock()
		r.errors[name] = err
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	delete(r.errors, name)
	r.mu.Unlock()
	return nil
}

func (r *Registry) findConfig(name string) (ServerConfig, bool) {
	for _, cfg := range r.configs {
		if cfg.Name == name {
			return cfg, true
		}
	}
	return ServerConfig{}, false
}

// RegisterTools registers all MCP tools into the Lcoder tools registry.
func (r *Registry) RegisterTools(registry *tools.Registry) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, client := range r.clients {
		for _, tool := range client.Tools() {
			exec := NewExecutable(client, tool)
			registry.Register(exec.Definition().Name, exec)
		}
	}
}

// ServerStatus describes the status of one MCP server.
type ServerStatus struct {
	Name      string
	Transport string
	Command   []string
	URL       string
	Connected bool
	ToolCount int
	Info      Info
	Error     string
}

// Servers returns status info for each configured server.
func (r *Registry) Servers() []ServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var statuses []ServerStatus
	for _, cfg := range r.configs {
		client, ok := r.clients[cfg.Name]
		status := ServerStatus{
			Name:      cfg.Name,
			Transport: cfg.Transport,
			Command:   cfg.Command,
			URL:       cfg.URL,
		}
		if ok && client.Healthy() {
			status.Connected = true
			status.ToolCount = len(client.Tools())
			status.Info = client.ServerInfo()
		} else if err, ok := r.errors[cfg.Name]; ok {
			status.Error = err.Error()
		} else {
			status.Error = "not connected"
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// PrefixedName returns a tool name with the server prefix.
func PrefixedName(serverName, toolName string) string {
	return fmt.Sprintf("%s_%s", serverName, toolName)
}
