# Add Unified Config Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Catch invalid configuration at startup with clear, field-level error messages instead of failing later at runtime.

**Architecture:** Add `Config.Validate()` in `pkg/config/config.go`. Call it immediately after `Load()` in `cmd/lcoder/main.go`. Validate provider, model, sandbox backend, numeric ranges, and permission decisions.

**Tech Stack:** Go 1.25, `pkg/config`, `pkg/permissions`, `pkg/sandbox`.

---

## File Structure

- **Modify:** `pkg/config/config.go`
- **Create:** `pkg/config/validate_test.go`
- **Modify:** `cmd/lcoder/main.go`

---

## Task 1: Implement Config.Validate

**Files:**
- Modify: `pkg/config/config.go`
- Create: `pkg/config/validate_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/config/validate_test.go`:

```go
package config

import "testing"

func TestValidate_InvalidSandboxBackend(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sandbox.Backend = "container" // not yet supported per current policy
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for unsupported sandbox backend")
	}
}

func TestValidate_InvalidPermissionDecision(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Permissions.Rules["read"] = map[string]string{"*": "allow_all"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for invalid decision")
	}
}

func TestValidate_ValidDefault(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
}
```

Run:
```bash
go test ./pkg/config/ -run TestValidate -v
```
Expected: FAIL — `Validate` does not exist.

- [ ] **Step 2: Implement Validate**

Append to `pkg/config/config.go`:

```go
// Validate returns an error if the configuration is inconsistent or invalid.
func (c Config) Validate() error {
	if c.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if err := c.Sandbox.validate(); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if err := c.Context.validate(); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	if err := c.Permissions.validate(); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	return nil
}

func (s SandboxConfig) validate() error {
	switch s.Backend {
	case "", "passthrough", "soft-limit":
		return nil
	case "container", "remote":
		return fmt.Errorf("backend %q is reserved and not yet implemented", s.Backend)
	default:
		return fmt.Errorf("backend %q is unknown", s.Backend)
	}
}

func (c ContextConfig) validate() error {
	if c.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must be >= 0")
	}
	if c.TargetTokens < 0 {
		return fmt.Errorf("target_tokens must be >= 0")
	}
	if c.ReserveOutput < 0 {
		return fmt.Errorf("reserve_output must be >= 0")
	}
	if c.MaxOutput < 0 {
		return fmt.Errorf("max_output must be >= 0")
	}
	if c.CompactThreshold < 0 || c.CompactThreshold > 1 {
		return fmt.Errorf("compact_threshold must be in [0,1]")
	}
	if c.DropThreshold < 0 || c.DropThreshold > 1 {
		return fmt.Errorf("drop_threshold must be in [0,1]")
	}
	return nil
}

func (p PermissionConfig) validate() error {
	for tool, table := range p.Rules {
		for pattern, decision := range table {
			switch decision {
			case "allow", "ask", "deny":
			default:
				return fmt.Errorf("rule for %s/%q has invalid decision %q", tool, pattern, decision)
			}
		}
	}
	return nil
}
```

Run:
```bash
go test ./pkg/config/ -run TestValidate -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/config/config.go pkg/config/validate_test.go
git commit -m "feat(config): Config.Validate with field-level errors

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Call Validate at Startup

**Files:**
- Modify: `cmd/lcoder/main.go`

- [ ] **Step 1: Insert validation after Load**

In `cmd/lcoder/main.go`, after `cfg, err := config.Load()`:

```go
if err := cfg.Validate(); err != nil {
	return nil, fmt.Errorf("invalid config: %w", err)
}
```

- [ ] **Step 2: Commit**

```bash
git add cmd/lcoder/main.go
git commit -m "feat(cli): validate config immediately after load

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: Full Verification

- [ ] **Step 1: Run config tests**

```bash
go test ./pkg/config/... -count=1
```
Expected: PASS.

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./pkg/config/... ./cmd/lcoder/...
```
Expected: no output.

---

## Self-review

1. **Spec coverage:**
   - Validate method: Task 1
   - Startup call: Task 2

2. **Placeholder scan:** No TBD/TODO.

3. **Type consistency:** Validation helpers are value receivers on config structs.
