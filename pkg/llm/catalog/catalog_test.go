// pkg/llm/catalog/catalog_test.go
package catalog

import "testing"

func TestSnapshotLoads(t *testing.T) {
	c := New(Options{Refresh: false})
	list := c.List()
	if len(list) < 6 {
		t.Fatalf("expected >=6 snapshot models, got %d", len(list))
	}
}

func TestWindowExactAndPrefix(t *testing.T) {
	c := New(Options{Refresh: false})
	if w := c.Window("openai", "gpt-4o"); w != 128000 {
		t.Errorf("gpt-4o window=%d", w)
	}
	// Dated Anthropic id resolves by prefix to the base catalog entry.
	if w := c.Window("anthropic", "claude-sonnet-4-20250514"); w != 200000 {
		t.Errorf("sonnet window=%d", w)
	}
}

func TestPriceTable(t *testing.T) {
	c := New(Options{Refresh: false})
	pt := c.PriceTable()
	if p, ok := pt["openai/gpt-4o"]; !ok || p.Prompt != 2.50 {
		t.Fatalf("price table missing gpt-4o: %+v", pt["openai/gpt-4o"])
	}
}

func TestOverrideWins(t *testing.T) {
	c := New(Options{Refresh: false, Overrides: []Entry{
		{ID: "gpt-4o", Provider: "openai", ContextWindow: 999},
	}})
	if w := c.Window("openai", "gpt-4o"); w != 999 {
		t.Errorf("override window=%d, want 999", w)
	}
}

func TestSnapshotMaxOutput(t *testing.T) {
	c := New(Options{Refresh: false})
	if got := c.MaxOutput("openai", "gpt-4o"); got != 16384 {
		t.Errorf("gpt-4o MaxOutput=%d, want 16384", got)
	}
	if got := c.MaxOutput("anthropic", "claude-sonnet-4-20250514"); got != 64000 {
		t.Errorf("claude sonnet MaxOutput=%d, want 64000", got)
	}
	if got := c.MaxOutput("gemini", "gemini-2.5-pro"); got != 65536 {
		t.Errorf("gemini MaxOutput=%d, want 65536", got)
	}
}

func TestProviderAliasGemini(t *testing.T) {
	c := New(Options{Refresh: false, Overrides: []Entry{
		{ID: "gemini-2.5-flash", Provider: "google", ContextWindow: 1048576, MaxOutput: 65536},
	}})
	if w := c.Window("gemini", "gemini-2.5-flash"); w != 1048576 {
		t.Errorf("gemini alias window=%d, want 1048576", w)
	}
	if out := c.MaxOutput("gemini", "gemini-2.5-flash"); out != 65536 {
		t.Errorf("gemini alias MaxOutput=%d, want 65536", out)
	}
}
