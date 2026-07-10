package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// catalogFor builds a Config whose catalog declares one model with the given
// window/budget, useful for exercising the catalog layer of the priority chain.
func catalogFor(model string, window, target, reserve int) Config {
	cfg := DefaultConfig()
	cfg.Model = model
	cfg.Catalog = ModelCatalog{Models: []ModelMeta{{
		ID:            model,
		ContextWindow: window,
		Budget:        ModelBudget{Target: target, ReserveOutput: reserve},
	}}}
	return cfg
}

// With no user config, no catalog, and no discovered window, resolution falls
// back to the hard defaults and reports source "default".
func TestDefaultBashPermissionRules(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Permissions.UnsafeMode {
		t.Fatal("expected UnsafeMode to default to false")
	}

	bashRules := cfg.Permissions.Rules["bash"]

	cases := []struct {
		pattern  string
		decision string
	}{
		{"*", "ask"},
		{"ls *", "allow"},
		{"cat *", "allow"},
		{"grep *", "allow"},
		{"git status", "allow"},
		{"git log", "allow"},
		{"git diff", "allow"},
		{"go test *", "allow"},
		{"go vet *", "allow"},
		{"rm -rf /", "deny"},
		{"rm -rf /*", "deny"},
		{"sudo *", "deny"},
		{"mkfs.*", "deny"},
		{"dd *", "deny"},
	}
	for _, c := range cases {
		if got := bashRules[c.pattern]; got != c.decision {
			t.Errorf("bash rule %q: got %q, want %q", c.pattern, got, c.decision)
		}
	}
}

func TestResolveContextBudgetDefaultFallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model = "mystery-model"
	budget, source := cfg.ResolveContextBudget(0, 0)

	if source != "default" {
		t.Fatalf("expected source default, got %q", source)
	}
	if budget.MaxTotal != fallbackMaxTokens {
		t.Fatalf("expected max %d, got %d", fallbackMaxTokens, budget.MaxTotal)
	}
	if budget.ReserveOutput != defaultReserveOutput {
		t.Fatalf("expected reserve %d, got %d", defaultReserveOutput, budget.ReserveOutput)
	}
	if budget.TargetTotal != int(float64(fallbackMaxTokens)*defaultTargetRatio) {
		t.Fatalf("expected derived target, got %d", budget.TargetTotal)
	}
}

// A discovered window is used when neither user nor catalog supplies one.
func TestResolveContextBudgetDiscoveredWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model = "mystery-model"
	budget, source := cfg.ResolveContextBudget(256000, 0)

	if source != "discovered" {
		t.Fatalf("expected source discovered, got %q", source)
	}
	if budget.MaxTotal != 256000 {
		t.Fatalf("expected max 256000, got %d", budget.MaxTotal)
	}
	// target derived from the discovered window, reserve from default.
	if budget.TargetTotal != int(256000*defaultTargetRatio) {
		t.Fatalf("expected derived target, got %d", budget.TargetTotal)
	}
	if budget.ReserveOutput != defaultReserveOutput {
		t.Fatalf("expected reserve %d, got %d", defaultReserveOutput, budget.ReserveOutput)
	}
}

// The catalog window overrides the discovered window (explicit per-model override).
func TestResolveContextBudgetCatalogOverridesDiscovered(t *testing.T) {
	cfg := catalogFor("claude-sonnet-4-20250514", 200000, 180000, 8192)
	budget, source := cfg.ResolveContextBudget(999999, 0)

	if source != "catalog" {
		t.Fatalf("expected source catalog, got %q", source)
	}
	if budget.MaxTotal != 200000 || budget.TargetTotal != 180000 || budget.ReserveOutput != 8192 {
		t.Fatalf("expected catalog budget, got %+v", budget)
	}
}

// User global config wins over both catalog and the discovered window.
func TestResolveContextBudgetUserOverrides(t *testing.T) {
	cfg := catalogFor("claude-sonnet-4-20250514", 200000, 180000, 8192)
	cfg.Context.MaxTokens = 64000
	cfg.Context.TargetTokens = 60000
	cfg.Context.ReserveOutput = 4096

	budget, source := cfg.ResolveContextBudget(999999, 0)
	if source != "user" {
		t.Fatalf("expected source user, got %q", source)
	}
	if budget.MaxTotal != 64000 || budget.TargetTotal != 60000 || budget.ReserveOutput != 4096 {
		t.Fatalf("expected user budget, got %+v", budget)
	}
}

// When only the catalog window is known (no budget target), the target is
// derived from the window and reserve falls back to the default.
func TestResolveContextBudgetCatalogWindowOnly(t *testing.T) {
	cfg := catalogFor("local-model", 100000, 0, 0)
	budget, source := cfg.ResolveContextBudget(0, 0)

	if source != "catalog" {
		t.Fatalf("expected source catalog, got %q", source)
	}
	if budget.MaxTotal != 100000 {
		t.Fatalf("expected max 100000, got %d", budget.MaxTotal)
	}
	if budget.TargetTotal != int(100000*defaultTargetRatio) {
		t.Fatalf("expected derived target, got %d", budget.TargetTotal)
	}
	if budget.ReserveOutput != defaultReserveOutput {
		t.Fatalf("expected reserve %d, got %d", defaultReserveOutput, budget.ReserveOutput)
	}
}

// A user target larger than the resolved max is clamped to the derived ratio.
func TestResolveContextBudgetTargetClamped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Context.MaxTokens = 100000
	cfg.Context.TargetTokens = 200000 // invalid: exceeds max
	budget, _ := cfg.ResolveContextBudget(0, 0)

	if budget.TargetTotal >= budget.MaxTotal {
		t.Fatalf("expected target < max, got target=%d max=%d", budget.TargetTotal, budget.MaxTotal)
	}
}

func TestResolveContextBudgetCompactThresholdDefault(t *testing.T) {
	cfg := DefaultConfig()
	budget, _ := cfg.ResolveContextBudget(0, 0)
	if budget.CompactThreshold != 0.9 {
		t.Fatalf("expected default compact threshold 0.9, got %v", budget.CompactThreshold)
	}
}

func TestResolveContextBudgetCompactThresholdOverride(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Context.CompactThreshold = 0.75
	budget, _ := cfg.ResolveContextBudget(0, 0)
	if budget.CompactThreshold != 0.75 {
		t.Fatalf("expected compact threshold 0.75, got %v", budget.CompactThreshold)
	}
}

func TestResolveContextBudgetCompactThresholdInvalidFallback(t *testing.T) {
	for _, bad := range []float64{0, -0.5, 1.5} {
		cfg := DefaultConfig()
		cfg.Context.CompactThreshold = bad
		budget, _ := cfg.ResolveContextBudget(0, 0)
		if budget.CompactThreshold != 0.9 {
			t.Fatalf("expected fallback 0.9 for invalid %v, got %v", bad, budget.CompactThreshold)
		}
	}
}

func TestResolveContextBudgetDropThreshold(t *testing.T) {
	cfg := Config{Context: ContextConfig{MaxTokens: 100000, DropThreshold: 0.8}}
	b, _ := cfg.ResolveContextBudget(0, 0)
	if b.DropThreshold != 0.8 {
		t.Fatalf("expected DropThreshold 0.8, got %v", b.DropThreshold)
	}

	cfg.Context.DropThreshold = 0
	b, _ = cfg.ResolveContextBudget(0, 0)
	if b.DropThreshold != 1.0 {
		t.Fatalf("expected default DropThreshold 1.0, got %v", b.DropThreshold)
	}

	cfg.Context.DropThreshold = 1.5
	b, _ = cfg.ResolveContextBudget(0, 0)
	if b.DropThreshold != 1.0 {
		t.Fatalf("expected clamped DropThreshold 1.0, got %v", b.DropThreshold)
	}
}

// MaxOutput ceiling resolution: catalog budget.max_output wins over discovered,
// discovered wins over the fallback constant, and an explicit user cap clamps
// the result but never raises it above the model's physical ceiling.
func TestResolveContextBudgetMaxOutput(t *testing.T) {
	// Nothing known anywhere -> fallback ceiling.
	cfg := DefaultConfig()
	cfg.Model = "mystery"
	b, _ := cfg.ResolveContextBudget(0, 0)
	if b.MaxOutput != fallbackOutputCeiling {
		t.Fatalf("expected fallback ceiling %d, got %d", fallbackOutputCeiling, b.MaxOutput)
	}

	// Discovered ceiling used when catalog is silent.
	b, _ = cfg.ResolveContextBudget(0, 8000)
	if b.MaxOutput != 8000 {
		t.Fatalf("expected discovered ceiling 8000, got %d", b.MaxOutput)
	}

	// Catalog budget.max_output overrides discovered.
	cfg.Catalog = ModelCatalog{Models: []ModelMeta{{
		ID:     "mystery",
		Budget: ModelBudget{MaxOutput: 32000},
	}}}
	b, _ = cfg.ResolveContextBudget(0, 8000)
	if b.MaxOutput != 32000 {
		t.Fatalf("expected catalog ceiling 32000, got %d", b.MaxOutput)
	}

	// User cap below the ceiling clamps down.
	cfg.Context.MaxOutput = 4096
	b, _ = cfg.ResolveContextBudget(0, 8000)
	if b.MaxOutput != 4096 {
		t.Fatalf("expected user cap 4096, got %d", b.MaxOutput)
	}

	// User cap above the physical ceiling is ignored (never exceed the model).
	cfg.Context.MaxOutput = 100000
	b, _ = cfg.ResolveContextBudget(0, 8000)
	if b.MaxOutput != 32000 {
		t.Fatalf("expected clamp to ceiling 32000, got %d", b.MaxOutput)
	}
}

func TestMCPServerConfigRemote(t *testing.T) {
	data := `
mcp_servers:
  - name: remote
    transport: sse
    url: http://localhost:3000
    headers:
      Authorization: Bearer token
    timeout: 60
  - name: local
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem"]
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.MCPServers))
	}
	remote := cfg.MCPServers[0]
	if remote.Name != "remote" || remote.Transport != "sse" || remote.URL != "http://localhost:3000" {
		t.Fatalf("unexpected remote config: %+v", remote)
	}
	if remote.Headers["Authorization"] != "Bearer token" {
		t.Fatalf("unexpected headers: %+v", remote.Headers)
	}
	if remote.Timeout != 60 {
		t.Fatalf("expected timeout 60, got %d", remote.Timeout)
	}
	local := cfg.MCPServers[1]
	if local.Name != "local" || local.Transport != "stdio" || len(local.Command) == 0 {
		t.Fatalf("unexpected local config: %+v", local)
	}
}
