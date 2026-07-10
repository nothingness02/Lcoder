package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/models"
)

type fakeToolExtension struct {
	register func(registry *Registry, cwd string) error
}

func (f *fakeToolExtension) RegisterTools(registry *Registry, cwd string) error {
	return f.register(registry, cwd)
}

type fakePluginLoader struct {
	ext ToolExtension
	err error
}

func (f *fakePluginLoader) LoadPlugin(path string, cfg map[string]any) (ToolExtension, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.ext, nil
}

func TestLoadExtensionsJSONDescriptor(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "echo.json")
	data := `{
		"name": "echo",
		"endpoint": "http://localhost:8080/echo",
		"description": "echo tool",
		"parameters": {"type": "object"},
		"execution_mode": "sequential",
		"headers": {"X-Tool": "echo"}
	}`
	if err := os.WriteFile(desc, []byte(data), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	r := NewRegistry(dir)
	if err := r.LoadExtensions([]config.ToolExtensionConfig{{Path: desc}}, nil); err != nil {
		t.Fatalf("LoadExtensions: %v", err)
	}

	exec, ok := r.Get("echo")
	if !ok {
		t.Fatal("expected echo tool to be registered")
	}
	def := exec.Definition()
	if def.Description != "echo tool" {
		t.Errorf("description = %q, want echo tool", def.Description)
	}
	if def.ExecutionMode != models.ExecutionSequential {
		t.Errorf("execution mode = %q, want sequential", def.ExecutionMode)
	}
}

func TestLoadExtensionsJSONOverrides(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "tool.json")
	data := `{
		"name": "ignored",
		"endpoint": "http://old",
		"description": "old desc",
		"parameters": {"type": "object"},
		"execution_mode": "parallel",
		"headers": {"X-Old": "old"}
	}`
	if err := os.WriteFile(desc, []byte(data), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	r := NewRegistry(dir)
	cfg := config.ToolExtensionConfig{
		Name:          "overridden",
		Path:          desc,
		Endpoint:      "http://new",
		Description:   "new desc",
		Parameters:    map[string]any{"type": "array"},
		ExecutionMode: "sequential",
		Headers:       map[string]string{"X-New": "new"},
	}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}, nil); err != nil {
		t.Fatalf("LoadExtensions: %v", err)
	}

	exec, ok := r.Get("overridden")
	if !ok {
		t.Fatal("expected overridden tool to be registered")
	}
	def := exec.Definition()
	if def.Description != "new desc" {
		t.Errorf("description = %q, want new desc", def.Description)
	}
	if def.ExecutionMode != models.ExecutionSequential {
		t.Errorf("execution mode = %q, want sequential", def.ExecutionMode)
	}
}

func TestLoadExtensionsJSONDefaultsToJSONType(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "tool.json")
	if err := os.WriteFile(desc, []byte(`{"name":"plain"}`), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	r := NewRegistry(dir)
	if err := r.LoadExtensions([]config.ToolExtensionConfig{{Path: desc}}, nil); err != nil {
		t.Fatalf("LoadExtensions: %v", err)
	}
	if _, ok := r.Get("plain"); !ok {
		t.Fatal("expected plain tool to be registered")
	}
}

func TestLoadExtensionsJSONMissingPath(t *testing.T) {
	r := NewRegistry(".")
	cfg := config.ToolExtensionConfig{Name: "missing", Type: "json"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}, nil); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestLoadExtensionsGoPluginSuccess(t *testing.T) {
	r := NewRegistry(".")
	loader := &fakePluginLoader{
		ext: &fakeToolExtension{
			register: func(registry *Registry, cwd string) error {
				registry.Register("plugin-tool", NewHTTPExecutable(HTTPConfig{Name: "plugin-tool", Endpoint: "http://x"}))
				return nil
			},
		},
	}
	cfg := config.ToolExtensionConfig{Name: "ext", Type: "go-plugin", Path: "ext.so"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}, loader); err != nil {
		t.Fatalf("LoadExtensions: %v", err)
	}
	if _, ok := r.Get("plugin-tool"); !ok {
		t.Fatal("expected plugin-tool to be registered")
	}
}

func TestLoadExtensionsGoPluginLoaderError(t *testing.T) {
	r := NewRegistry(".")
	loader := &fakePluginLoader{err: errors.New("boom")}
	cfg := config.ToolExtensionConfig{Name: "ext", Type: "go-plugin", Path: "ext.so"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}, loader); err == nil {
		t.Fatal("expected error from plugin loader")
	}
}

func TestLoadExtensionsGoPluginMissingLoader(t *testing.T) {
	r := NewRegistry(".")
	cfg := config.ToolExtensionConfig{Name: "ext", Type: "go-plugin", Path: "ext.so"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}, nil); err == nil {
		t.Fatal("expected error when plugin loader is nil")
	}
}

func TestLoadExtensionsUnknownType(t *testing.T) {
	r := NewRegistry(".")
	cfg := config.ToolExtensionConfig{Name: "ext", Type: "unknown"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}, nil); err == nil {
		t.Fatal("expected error for unknown extension type")
	}
}
