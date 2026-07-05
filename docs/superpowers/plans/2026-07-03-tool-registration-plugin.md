# Plugin-Driven Tool Registration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow external tools to be registered from a config-defined directory or JSON descriptor without modifying the main binary.

**Architecture:** Add a `ToolExtensionConfig` to `config.yaml`. Implement `pkg/tools/extensions.go` that scans a directory for `.json` tool descriptors or `.so`/Go plugin files. JSON descriptors describe an HTTP-like tool endpoint; plugins export a factory function. Keep the existing `init()` mechanism as-is for built-ins.

**Tech Stack:** Go 1.25, `pkg/tools`, `pkg/config`.

---

## File Structure

- **Modify:** `pkg/config/config.go`
- **Create:** `pkg/tools/extensions.go`
- **Create:** `pkg/tools/extensions_test.go`
- **Modify:** `cmd/lcoder/main.go`

---

## Task 1: Add Tool Extension Config

**Files:**
- Modify: `pkg/config/config.go`

- [ ] **Step 1: Add config types**

Add to `pkg/config/config.go`:

```go
// ToolExtensionConfig describes an external tool source.
type ToolExtensionConfig struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"` // file path to .json descriptor or .so plugin
}
```

Add `ToolExtensions []ToolExtensionConfig` to `Config`.

- [ ] **Step 2: Commit**

```bash
git add pkg/config/config.go
git commit -m "feat(config): tool extension source config

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Load JSON Tool Descriptors

**Files:**
- Create: `pkg/tools/extensions.go`
- Create: `pkg/tools/extensions_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/tools/extensions_test.go`:

```go
package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExtensionDescriptor(t *testing.T) {
	dir := t.TempDir()
	desc := `{
		"name": "weather",
		"description": "Get weather",
		"endpoint": "https://api.example.com/weather",
		"parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]},
		"execution_mode": "parallel"
	}`
	path := filepath.Join(dir, "weather.json")
	_ = os.WriteFile(path, []byte(desc), 0o644)

	reg := NewRegistry(".")
	if err := LoadExtension(reg, ExtensionConfig{Path: path}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reg.Has("weather") {
		t.Fatal("expected weather tool registered")
	}
}
```

Run:
```bash
go test ./pkg/tools/ -run TestLoadExtensionDescriptor -v
```
Expected: FAIL — `LoadExtension` does not exist.

- [ ] **Step 2: Implement extension loader**

Create `pkg/tools/extensions.go`:

```go
package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lcoder/lcoder/pkg/models"
)

// ExtensionConfig describes an external tool source.
type ExtensionConfig struct {
	Name string
	Path string
}

// LoadExtension loads a tool extension from path into registry.
func LoadExtension(r *Registry, cfg ExtensionConfig) error {
	ext := filepath.Ext(cfg.Path)
	switch ext {
	case ".json":
		return loadJSONExtension(r, cfg.Path)
	default:
		return fmt.Errorf("unsupported extension type %q", ext)
	}
}

func loadJSONExtension(r *Registry, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var desc struct {
		Name          string            `json:"name"`
		Description   string            `json:"description"`
		Endpoint      string            `json:"endpoint"`
		Parameters    map[string]any    `json:"parameters"`
		ExecutionMode string            `json:"execution_mode"`
		Headers       map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(data, &desc); err != nil {
		return err
	}
	mode := models.ExecutionMode(desc.ExecutionMode)
	if mode == "" {
		mode = models.ExecutionParallel
	}
	r.Register(desc.Name, NewHTTPExecutable(HTTPConfig{
		Name:          desc.Name,
		Endpoint:      ExpandEndpointEnv(desc.Endpoint),
		Description:   desc.Description,
		Parameters:    desc.Parameters,
		ExecutionMode: mode,
		Headers:       desc.Headers,
	}))
	return nil
}
```

Run:
```bash
go test ./pkg/tools/ -run TestLoadExtensionDescriptor -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/tools/extensions.go pkg/tools/extensions_test.go
git commit -m "feat(tools): JSON descriptor tool extensions

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Wire Extensions in prepareAgent

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: Load extensions**

After `registry.RegisterBuiltinFactories(cwd)`:

```go
for _, ext := range cfg.ToolExtensions {
	if err := tools.LoadExtension(registry, tools.ExtensionConfig{Name: ext.Name, Path: ext.Path}); err != nil {
		return nil, fmt.Errorf("load tool extension %q: %w", ext.Name, err)
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add cmd/lcoder/main.go
git commit -m "feat(cli): load tool extensions at startup

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Full Verification

- [ ] **Step 1: Run tests**

```bash
go test ./pkg/tools/... ./pkg/config/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/tools/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Config type: Task 1
   - JSON loader: Task 2
   - Wiring: Task 3

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** `ExtensionConfig` in `pkg/tools` mirrors `ToolExtensionConfig` in `pkg/config`.
