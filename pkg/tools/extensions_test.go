package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
)

func TestLoadExtensionsJSONDescriptor(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "echo.json")
	data := `{
		"name": "echo",
		"endpoint": "http://localhost:8080/echo",
		"description": "echo tool",
		"parameters": {"type": "object"},
		"headers": {"X-Tool": "echo"}
	}`
	if err := os.WriteFile(desc, []byte(data), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	r := NewRegistry(dir)
	if err := r.LoadExtensions([]config.ToolExtensionConfig{{Path: desc}}); err != nil {
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
}

func TestLoadExtensionsJSONOverrides(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "tool.json")
	data := `{
		"name": "ignored",
		"endpoint": "http://old",
		"description": "old desc",
		"parameters": {"type": "object"},
		"headers": {"X-Old": "old"}
	}`
	if err := os.WriteFile(desc, []byte(data), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	r := NewRegistry(dir)
	cfg := config.ToolExtensionConfig{
		Name:        "overridden",
		Path:        desc,
		Endpoint:    "http://new",
		Description: "new desc",
		Parameters:  map[string]any{"type": "array"},
		Headers:     map[string]string{"X-New": "new"},
	}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}); err != nil {
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
}

func TestLoadExtensionsJSONDefaultsToJSONType(t *testing.T) {
	dir := t.TempDir()
	desc := filepath.Join(dir, "tool.json")
	if err := os.WriteFile(desc, []byte(`{"name":"plain"}`), 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	r := NewRegistry(dir)
	if err := r.LoadExtensions([]config.ToolExtensionConfig{{Path: desc}}); err != nil {
		t.Fatalf("LoadExtensions: %v", err)
	}
	if _, ok := r.Get("plain"); !ok {
		t.Fatal("expected plain tool to be registered")
	}
}

func TestLoadExtensionsJSONMissingPath(t *testing.T) {
	r := NewRegistry(".")
	cfg := config.ToolExtensionConfig{Name: "missing", Type: "json"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestLoadExtensionsUnknownType(t *testing.T) {
	r := NewRegistry(".")
	cfg := config.ToolExtensionConfig{Name: "ext", Type: "unknown"}
	if err := r.LoadExtensions([]config.ToolExtensionConfig{cfg}); err == nil {
		t.Fatal("expected error for unknown extension type")
	}
}
