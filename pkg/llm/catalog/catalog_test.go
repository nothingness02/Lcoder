// pkg/llm/catalog/catalog_test.go
package catalog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	// (models.dev 不再单列 claude-sonnet-4;基准条目是 claude-sonnet-4-5。)
	if w := c.Window("anthropic", "claude-sonnet-4-5-20260514"); w != 1000000 {
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
	if got := c.MaxOutput("anthropic", "claude-sonnet-4-5"); got != 64000 {
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

// models.dev 风格 api.json 的一段,覆盖:limit.input、modalities、status 过滤、
// embedding 过滤、reasoning_options 三形态(effort+none / toggle / 无)。
const sampleAPIJSON = `{
  "openai": {
    "id": "openai",
    "npm": "@ai-sdk/openai",
    "api": "https://api.openai.com/v1",
    "env": ["OPENAI_API_KEY"],
    "models": {
      "gpt-5": {
        "name": "GPT-5",
        "limit": {"context": 400000, "input": 272000, "output": 128000},
        "modalities": {"input": ["text", "image"], "output": ["text"]},
        "tool_call": true,
        "reasoning": true,
        "reasoning_options": [{"type": "effort", "values": ["low", "medium", "high", null]}],
        "cost": {"input": 1.25, "output": 10}
      },
      "gpt-4o": {
        "name": "GPT-4o",
        "limit": {"context": 128000, "output": 16384},
        "modalities": {"input": ["text", "image", "audio"], "output": ["text"]},
        "tool_call": true,
        "cost": {"input": 2.5, "output": 10}
      },
      "gpt-5-mini-alpha": {
        "name": "alpha model",
        "status": "alpha",
        "limit": {"context": 100000, "output": 1000},
        "modalities": {"input": ["text"], "output": ["text"]}
      },
      "old-model": {
        "name": "deprecated model",
        "status": "deprecated",
        "limit": {"context": 100000, "output": 1000},
        "modalities": {"input": ["text"], "output": ["text"]}
      },
      "text-embedding-3-large": {
        "name": "embed",
        "family": "embedding",
        "limit": {"context": 8191, "output": 0},
        "modalities": {"input": ["text"], "output": ["embedding"]}
      },
      "tts-1": {
        "name": "tts",
        "limit": {"context": 1000, "output": 0},
        "modalities": {"input": ["text"], "output": ["audio"]}
      }
    }
  },
  "xai": {
    "id": "xai",
    "npm": "@ai-sdk/xai",
    "api": "https://api.x.ai/v1",
    "models": {
      "grok-4": {
        "name": "Grok 4",
        "limit": {"context": 256000, "output": 64000},
        "modalities": {"input": ["text"], "output": ["text"]},
        "tool_call": true,
        "reasoning": true,
        "reasoning_options": [{"type": "effort", "values": ["low", "high", "none"]}]
      },
      "grok-toggle": {
        "name": "Grok Toggle",
        "limit": {"context": 100000, "output": 1000},
        "modalities": {"input": ["text"], "output": ["text"]},
        "reasoning": true,
        "reasoning_options": [{"type": "toggle"}]
      }
    }
  }
}`

func serveAndFetch(t *testing.T) (Dataset, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sampleAPIJSON))
	}))
	defer srv.Close()
	return FetchEntries(srv.URL)
}

func TestFetchEntriesParsesExtendedFields(t *testing.T) {
	ds, err := serveAndFetch(t)
	if err != nil {
		t.Fatalf("FetchEntries: %v", err)
	}
	var gpt5 *Entry
	for i := range ds.Models {
		if ds.Models[i].ID == "gpt-5" {
			gpt5 = &ds.Models[i]
		}
	}
	if gpt5 == nil {
		t.Fatal("gpt-5 missing from parsed models")
	}
	if gpt5.ContextWindow != 400000 || gpt5.MaxInput != 272000 || gpt5.MaxOutput != 128000 {
		t.Errorf("limits = %d/%d/%d, want 400000/272000/128000", gpt5.ContextWindow, gpt5.MaxInput, gpt5.MaxOutput)
	}
	for _, want := range []string{"tools", "reasoning", "vision"} {
		if !hasCap(gpt5.Capabilities, want) {
			t.Errorf("gpt-5 missing capability %q (has %v)", want, gpt5.Capabilities)
		}
	}
	if len(gpt5.Efforts) != 3 || gpt5.Efforts[0] != "low" || gpt5.Efforts[2] != "high" {
		t.Errorf("efforts = %v, want [low medium high]", gpt5.Efforts)
	}
	// JSON null tier → OffEffort "none"
	if gpt5.OffEffort != "none" {
		t.Errorf("off_effort = %q, want none", gpt5.OffEffort)
	}
}

func TestFetchEntriesFilters(t *testing.T) {
	ds, err := serveAndFetch(t)
	if err != nil {
		t.Fatalf("FetchEntries: %v", err)
	}
	for _, e := range ds.Models {
		switch e.ID {
		case "gpt-5-mini-alpha", "old-model":
			t.Errorf("status-filtered model %s should be filtered", e.ID)
		case "text-embedding-3-large":
			t.Errorf("embedding model should be filtered")
		case "tts-1":
			t.Errorf("non-text-output model should be filtered")
		}
	}
}

func TestFetchEntriesReasoningForms(t *testing.T) {
	ds, err := serveAndFetch(t)
	if err != nil {
		t.Fatalf("FetchEntries: %v", err)
	}
	var grok, toggle *Entry
	for i := range ds.Models {
		switch ds.Models[i].ID {
		case "grok-4":
			grok = &ds.Models[i]
		case "grok-toggle":
			toggle = &ds.Models[i]
		}
	}
	if grok == nil || toggle == nil {
		t.Fatal("grok entries missing")
	}
	if len(grok.Efforts) != 2 || grok.OffEffort != "none" {
		t.Errorf("grok-4 efforts=%v off=%q, want [low high]/none", grok.Efforts, grok.OffEffort)
	}
	if !toggle.ThinkingToggle || len(toggle.Efforts) != 0 {
		t.Errorf("grok-toggle toggle=%v efforts=%v, want true/[]", toggle.ThinkingToggle, toggle.Efforts)
	}
}

func TestFetchEntriesProviderMeta(t *testing.T) {
	ds, err := serveAndFetch(t)
	if err != nil {
		t.Fatalf("FetchEntries: %v", err)
	}
	var openai *ProviderMeta
	for i := range ds.Providers {
		if ds.Providers[i].ID == "openai" {
			openai = &ds.Providers[i]
		}
	}
	if openai == nil {
		t.Fatal("openai provider meta missing")
	}
	if openai.Npm != "@ai-sdk/openai" || openai.API != "https://api.openai.com/v1" {
		t.Errorf("meta = %+v", openai)
	}
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// merge 对同 key 的零值新字段保留既有值,非零值则覆盖。
func TestMergePreservesNewFields(t *testing.T) {
	c := New(Options{Refresh: false})
	key := "testp/testmodel"
	c.merge([]Entry{{
		ID: "testmodel", Provider: "testp",
		ContextWindow: 1000, MaxInput: 800, MaxOutput: 200,
		Efforts: []string{"low", "high"}, OffEffort: "none", ThinkingToggle: true,
	}})

	// 零值新字段不覆盖既有值。
	c.merge([]Entry{{ID: "testmodel", Provider: "testp", ContextWindow: 2000}})
	got := c.entries[key]
	if got.MaxInput != 800 {
		t.Errorf("MaxInput=%d, want preserved 800", got.MaxInput)
	}
	if len(got.Efforts) != 2 || got.Efforts[0] != "low" {
		t.Errorf("Efforts=%v, want preserved [low high]", got.Efforts)
	}
	if got.OffEffort != "none" {
		t.Errorf("OffEffort=%q, want preserved none", got.OffEffort)
	}
	if !got.ThinkingToggle {
		t.Error("ThinkingToggle should stay true")
	}
	if got.ContextWindow != 2000 {
		t.Errorf("ContextWindow=%d, want overridden 2000", got.ContextWindow)
	}

	// 非零新字段覆盖既有值。
	c.merge([]Entry{{
		ID: "testmodel", Provider: "testp",
		MaxInput: 900, Efforts: []string{"minimal"}, OffEffort: "off",
	}})
	got = c.entries[key]
	if got.MaxInput != 900 {
		t.Errorf("MaxInput=%d, want 900", got.MaxInput)
	}
	if len(got.Efforts) != 1 || got.Efforts[0] != "minimal" {
		t.Errorf("Efforts=%v, want [minimal]", got.Efforts)
	}
	if got.OffEffort != "off" {
		t.Errorf("OffEffort=%q, want off", got.OffEffort)
	}
}

// ProviderMeta 按别名解析:元数据登记在别名目标(google)下,用规范名(gemini)查询。
func TestProviderMetaAliasAware(t *testing.T) {
	c := New(Options{Refresh: false})
	c.mergeDataset(Dataset{Providers: []ProviderMeta{
		{ID: "google", Npm: "@ai-sdk/google", API: "https://generativelanguage.googleapis.com"},
	}})
	m, ok := c.ProviderMeta("gemini")
	if !ok {
		t.Fatal("ProviderMeta(gemini) should resolve via alias to google")
	}
	if m.Npm != "@ai-sdk/google" {
		t.Errorf("meta = %+v", m)
	}
	if _, ok := c.ProviderMeta("no-such-provider"); ok {
		t.Error("unknown provider should return ok=false")
	}
}

// ThinkingSpec 派生:efforts 无 off 无 toggle → always;anthropic wire 例外。
func TestThinkingSpec(t *testing.T) {
	c := New(Options{Refresh: false})
	c.mergeDataset(Dataset{Models: []Entry{
		{ID: "gpt-5", Provider: "openai", ContextWindow: 400000,
			Efforts: []string{"low", "medium", "high"}, OffEffort: ""}, // 无 toggle 无 off → always
		{ID: "grok-4", Provider: "xai", ContextWindow: 256000,
			Efforts: []string{"low", "high"}, OffEffort: "none"},
		{ID: "claude-sonnet-4", Provider: "anthropic", ContextWindow: 200000,
			Efforts: []string{"low", "high"}}, // anthropic wire → 不标 always
		{ID: "toggler", Provider: "openai", ContextWindow: 100000,
			Efforts: []string{"high"}, ThinkingToggle: true},
	}})
	if s := c.ThinkingSpec("openai", "openai", "gpt-5"); !s.AlwaysThinking {
		t.Error("gpt-5 (efforts, no off, no toggle) must be AlwaysThinking")
	}
	if s := c.ThinkingSpec("openai", "xai", "grok-4"); s.AlwaysThinking {
		t.Error("grok-4 has OffEffort, must not be AlwaysThinking")
	}
	if s := c.ThinkingSpec("anthropic", "anthropic", "claude-sonnet-4"); s.AlwaysThinking {
		t.Error("anthropic wire has protocol-level disable, never AlwaysThinking")
	}
	if s := c.ThinkingSpec("openai", "openai", "toggler"); s.AlwaysThinking {
		t.Error("toggle form means thinking can be disabled")
	}
	if s := c.ThinkingSpec("openai", "openai", "unknown-model"); s.Efforts != nil || s.AlwaysThinking {
		t.Errorf("unknown model must return zero spec, got %+v", s)
	}
}
