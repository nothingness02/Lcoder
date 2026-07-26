// pkg/llm/catalog/capability_test.go
package catalog

import "testing"

func TestLookupFallbackAnthropic(t *testing.T) {
	// 静态表只收录已确认的代际:claude-sonnet-5 应 miss
	if _, ok := LookupFallback("anthropic", "claude-sonnet-5-20260101"); ok {
		t.Error("claude-sonnet-5 must miss (unconfirmed generation)")
	}
	// 已知组命中:
	c, ok := LookupFallback("anthropic", "claude-sonnet-4-20250514")
	if !ok {
		t.Fatal("claude-sonnet-4-* should hit fallback")
	}
	for _, want := range []string{"tools", "reasoning", "vision"} {
		if !hasCap(c.Capabilities, want) {
			t.Errorf("claude-sonnet-4 fallback missing %q (has %v)", want, c.Capabilities)
		}
	}
	// claude-3 组:无 reasoning
	c, ok = LookupFallback("anthropic", "claude-3.5-sonnet")
	if !ok {
		t.Fatal("claude-3.5 should hit fallback")
	}
	if hasCap(c.Capabilities, "reasoning") {
		t.Errorf("claude-3.5 fallback should not declare reasoning (has %v)", c.Capabilities)
	}
	// 非 anthropic route 不匹配 claude 规则
	if _, ok = LookupFallback("openai", "claude-sonnet-4"); ok {
		t.Error("claude rules must be scoped to the anthropic route")
	}
}

func TestLookupFallbackOpenAIAndGemini(t *testing.T) {
	c, ok := LookupFallback("openai", "o4-mini-2025-04-16")
	if !ok {
		t.Fatal("o4-mini should hit reasoning rule")
	}
	if !hasCap(c.Capabilities, "reasoning") || !hasCap(c.Capabilities, "tools") {
		t.Errorf("o4-mini fallback = %v", c.Capabilities)
	}
	c, ok = LookupFallback("openai", "gpt-4o-2024-08-06")
	if !ok || !hasCap(c.Capabilities, "vision") {
		t.Errorf("gpt-4o fallback = %v ok=%v", c.Capabilities, ok)
	}
	c, ok = LookupFallback("openai", "gemini-2.5-pro-exp")
	if !ok {
		t.Fatal("gemini-2.5 should hit fallback")
	}
	for _, want := range []string{"tools", "reasoning", "vision"} {
		if !hasCap(c.Capabilities, want) {
			t.Errorf("gemini-2.5 fallback missing %q (has %v)", want, c.Capabilities)
		}
	}
	if _, ok := LookupFallback("openai", "some-random-model"); ok {
		t.Error("unknown model must miss")
	}
}

func TestWindowFallsBackToStaticTable(t *testing.T) {
	c := New(Options{Refresh: false}) // 快照里没有的模型
	if w := c.Window("anthropic", "claude-nonexistent-9"); w != 0 {
		t.Errorf("static table has no window; want 0, got %d", w)
	}
	// MaxInput 无静态表项,恒 0
	if v := c.MaxInput("openai", "gpt-4o"); v != 0 {
		t.Errorf("MaxInput fallback must be 0, got %d", v)
	}
}

// 目录命中优先于静态表;目录未命中时 Capabilities 回落 LookupFallback。
func TestCatalogHitPreferredOverFallback(t *testing.T) {
	// 重生后的 models.dev snapshot 里 gpt-4o 能力集与静态表相同([tools vision]),
	// 无法再区分来源,故用 override 注入标记能力 "streaming"(静态表没有),
	// 验证目录/override 命中优先于 LookupFallback。
	c := New(Options{Refresh: false, Overrides: []Entry{
		{ID: "gpt-4o", Provider: "openai", Capabilities: []string{"tools", "vision", "streaming"}},
	}})

	// 目录精确命中 gpt-4o:Window/Capabilities 用条目值。
	if w := c.Window("openai", "gpt-4o"); w != 128000 {
		t.Errorf("gpt-4o window = %d, want catalog value 128000", w)
	}
	caps := c.Capabilities("openai", "gpt-4o")
	if !hasCap(caps, "streaming") {
		t.Errorf("gpt-4o capabilities should come from the catalog entry (has %v)", caps)
	}

	// 目录未命中 o4-mini:Capabilities 回落静态表。
	caps = c.Capabilities("openai", "o4-mini")
	if !hasCap(caps, "reasoning") || !hasCap(caps, "tools") {
		t.Errorf("o4-mini capabilities should fall back to the static table (has %v)", caps)
	}

	// 目录与静态表都未命中:Capabilities 为 nil。
	if caps := c.Capabilities("openai", "totally-unknown-x"); caps != nil {
		t.Errorf("unknown model capabilities = %v, want nil", caps)
	}
}
