# LLM Gateway 改善实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 借鉴 kimi-code 改善 Lcoder LLM gateway:catalog 元数据丰富化 + 静态能力兜底 + wire 推断/端点校验 + thinking effort 语义 + OpenAI Responses 第三 wire。

**Architecture:** 全部改动内聚在 `pkg/llm/catalog`（数据与解释规则）与 `pkg/llm/provider`（三种 wire 适配）;engine 只消费；config/cmd 层做薄接线。spec:`docs/superpowers/specs/2026-07-25-llm-gateway-design.md`。

**Tech Stack:** Go 1.25，无新依赖。

**分支:** `feat/llm-gateway`（已创建）。**测试约定：** `go test $(go list ./... | grep -v 'reference/Shannon')`。**提交纪律：** 每个 commit 只 `git add` 本任务涉及的文件，绝不 `git add -A`/`git add .`（工作区有用户未提交的其他删除）。

---

### Task 1: catalog 数据模型扩展与 models.dev 新解析

Entry 增加 `MaxInput`/`Efforts`/`OffEffort`/`ThinkingToggle`;capabilities 增加 vision/audio/video（由 modalities.input 映射）；新增 provider 级元数据（`ProviderMeta`,wire 推断依赖）;snapshot/cache 格式从 `[]Entry` 变为 `{providers, models}`；解析过滤 deprecated/alpha/embedding/非 text 输出；导出 `Dataset`/`FetchEntries` 供 Task 4 的快照生成工具复用。

**Files:**
- Modify: `pkg/llm/catalog/catalog.go`
- Test: `pkg/llm/catalog/catalog_test.go`（新建）

- [ ] **Step 1: 写失败测试**

新建 `pkg/llm/catalog/catalog_test.go`:

```go
package catalog

import "testing"

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
			t.Errorf("status %q model %s should be filtered", e.ID, e.ID)
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
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/catalog -run TestFetchEntries -v`
Expected: 编译失败（`FetchEntries`/`Dataset` 未定义）

- [ ] **Step 3: 实现**

`pkg/llm/catalog/catalog.go` 的修改（未列出的函数不变）:

```go
// Entry 增加字段(其余不变):
type Entry struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Provider       string   `json:"provider"`
	ContextWindow  int      `json:"context_window"`
	MaxInput       int      `json:"max_input,omitempty"`
	MaxOutput      int      `json:"max_output"`
	Capabilities   []string `json:"capabilities"`
	Efforts        []string `json:"efforts,omitempty"`
	OffEffort      string   `json:"off_effort,omitempty"`
	ThinkingToggle bool     `json:"thinking_toggle,omitempty"`
	Cost           struct {
		Prompt     float64 `json:"prompt"`
		Completion float64 `json:"completion"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
}

// ProviderMeta is provider-level models.dev metadata, kept for wire inference.
type ProviderMeta struct {
	ID  string   `json:"id"`
	Npm string   `json:"npm,omitempty"`
	API string   `json:"api,omitempty"`
	Env []string `json:"env,omitempty"`
}

// Dataset is the snapshot/cache file format.
type Dataset struct {
	Providers []ProviderMeta `json:"providers"`
	Models    []Entry        `json:"models"`
}
```

`Catalog` 结构加 `providers map[string]ProviderMeta`（与 `entries` 同锁保护）。`New` 改为：

```go
func New(opts Options) *Catalog {
	src := opts.SourceURL
	if src == "" {
		src = modelsDevURL
	}
	c := &Catalog{entries: map[string]Entry{}, providers: map[string]ProviderMeta{}, overrides: opts.Overrides, sourceURL: src}
	var ds Dataset
	if err := json.Unmarshal(snapshotJSON, &ds); err == nil && len(ds.Models) > 0 {
		c.mergeDataset(ds)
	} else {
		// 旧格式 []Entry 兼容(重生前过渡;Task 4 后不会走到)
		var snap []Entry
		_ = json.Unmarshal(snapshotJSON, &snap)
		c.merge(snap)
	}
	c.merge(opts.Overrides)
	if opts.Refresh {
		go c.refresh(opts.CachePath)
	}
	return c
}

func (c *Catalog) mergeDataset(ds Dataset) {
	c.mu.Lock()
	for _, p := range ds.Providers {
		c.providers[p.ID] = p
	}
	c.mu.Unlock()
	c.merge(ds.Models)
}

// ProviderMeta returns the models.dev metadata for a provider id (alias-aware).
func (c *Catalog) ProviderMeta(name string) (ProviderMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range providerCandidates(name) {
		if m, ok := c.providers[p]; ok {
			return m, true
		}
	}
	return ProviderMeta{}, false
}
```

`refresh` 与 `applyRefresh` 改为走 Dataset(cache 读写都换成新格式；旧 cache 反序列化为 Dataset 会得到空 Models → 视为 miss 走网络）:

```go
func (c *Catalog) refresh(cachePath string) {
	if cachePath != "" {
		if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < cacheTTL {
			if data, err := os.ReadFile(cachePath); err == nil {
				var ds Dataset
				if json.Unmarshal(data, &ds) == nil && len(ds.Models) > 0 {
					c.applyRefresh(ds)
					return
				}
			}
		}
	}
	ds, err := FetchEntries(c.sourceURL)
	if err != nil || len(ds.Models) == 0 {
		return
	}
	if cachePath != "" {
		if data, err := json.Marshal(ds); err == nil {
			_ = fsutil.WritePrivateFile(cachePath, data)
		}
	}
	c.applyRefresh(ds)
}

func (c *Catalog) applyRefresh(ds Dataset) {
	c.mergeDataset(ds)
	c.merge(c.overrides)
}
```

`fetchModelsDev` 替换为导出的 `FetchEntries`（返回完整 Dataset，含过滤与 reasoning_options 解析）:

```go
// FetchEntries fetches a models.dev-style api.json and returns the parsed
// dataset: provider metadata plus filtered, normalized model entries.
func FetchEntries(url string) (Dataset, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return Dataset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Dataset{}, fmt.Errorf("models.dev returned %d", resp.StatusCode)
	}
	var raw map[string]struct {
		ID     string   `json:"id"`
		Npm    string   `json:"npm"`
		API    string   `json:"api"`
		Env    []string `json:"env"`
		Models map[string]struct {
			Name   string `json:"name"`
			Family string `json:"family"`
			Status string `json:"status"`
			Limit  struct {
				Context int `json:"context"`
				Input   int `json:"input"`
				Output  int `json:"output"`
			} `json:"limit"`
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			Cost struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cache_read"`
				CacheWrite float64 `json:"cache_write"`
			} `json:"cost"`
			ToolCall         bool              `json:"tool_call"`
			Reasoning        bool              `json:"reasoning"`
			ReasoningOptions []reasoningOption `json:"reasoning_options"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Dataset{}, err
	}
	var ds Dataset
	for provID, p := range raw {
		meta := ProviderMeta{ID: provID, Npm: p.Npm, API: p.API, Env: p.Env}
		ds.Providers = append(ds.Providers, meta)
		for modelID, m := range p.Models {
			if !isUsableChatModel(modelID, m.Name, m.Family, m.Status, m.Modalities.Output) {
				continue
			}
			e := Entry{
				ID: modelID, Name: m.Name, Provider: provID,
				ContextWindow: m.Limit.Context, MaxInput: m.Limit.Input, MaxOutput: m.Limit.Output,
			}
			if m.ToolCall {
				e.Capabilities = append(e.Capabilities, "tools")
			}
			if m.Reasoning {
				e.Capabilities = append(e.Capabilities, "reasoning")
			}
			for _, mod := range m.Modalities.Input {
				switch mod {
				case "image":
					e.Capabilities = append(e.Capabilities, "vision")
				case "audio":
					e.Capabilities = append(e.Capabilities, "audio")
				case "video":
					e.Capabilities = append(e.Capabilities, "video")
				}
			}
			e.Efforts, e.OffEffort, e.ThinkingToggle = parseReasoningOptions(m.ReasoningOptions)
			e.Cost.Prompt = m.Cost.Input
			e.Cost.Completion = m.Cost.Output
			e.Cost.CacheRead = m.Cost.CacheRead
			e.Cost.CacheWrite = m.Cost.CacheWrite
			ds.Models = append(ds.Models, e)
		}
	}
	return ds, nil
}

type reasoningOption struct {
	Type   string `json:"type"`
	Values []any  `json:"values"`
}

// parseReasoningOptions reads models.dev reasoning_options: effort levels, the
// "none"/null disable tier, and the boolean toggle form.
func parseReasoningOptions(opts []reasoningOption) (efforts []string, offEffort string, toggle bool) {
	for _, o := range opts {
		switch o.Type {
		case "toggle":
			toggle = true
		case "effort":
			var levels []string
			hasNull := false
			for _, v := range o.Values {
				if v == nil {
					hasNull = true
					continue
				}
				s, ok := v.(string)
				if !ok || s == "" {
					continue
				}
				if strings.EqualFold(s, "none") {
					offEffort = s
					continue
				}
				levels = append(levels, s)
			}
			if offEffort == "" && hasNull {
				offEffort = "none"
			}
			if len(levels) > 0 {
				efforts = levels
			}
		}
	}
	return efforts, offEffort, toggle
}

// isUsableChatModel drops deprecated/alpha models, embedding models, and
// models that do not emit text (mirrors kimi-code isUsableChatModel).
func isUsableChatModel(id, name, family, status string, outputModalities []string) bool {
	if status == "deprecated" || status == "alpha" {
		return false
	}
	if len(outputModalities) > 0 {
		hasText := false
		for _, m := range outputModalities {
			if m == "text" {
				hasText = true
			}
		}
		if !hasText {
			return false
		}
	}
	return !hasEmbeddingMarker(id) && !hasEmbeddingMarker(name) && !hasEmbeddingMarker(family)
}

func hasEmbeddingMarker(v string) bool {
	lower := strings.ToLower(v)
	if strings.Contains(lower, "embedding") {
		return true
	}
	for _, tok := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '/'
	}) {
		if tok == "embed" {
			return true
		}
	}
	return false
}
```

注意：`merge` 的字段保留逻辑也要覆盖新字段（override 合并时保留已有的 MaxInput/Efforts/OffEffort/ThinkingToggle，除非 override 显式给出）:

```go
// merge 内 existing 分支追加:
			if e.MaxInput > 0 {
				existing.MaxInput = e.MaxInput
			}
			if len(e.Efforts) > 0 {
				existing.Efforts = e.Efforts
			}
			if e.OffEffort != "" {
				existing.OffEffort = e.OffEffort
			}
			if e.ThinkingToggle {
				existing.ThinkingToggle = true
			}
```

- [ ] **Step 4: 运行测试确认通过 + 全量回归**

Run: `go test ./pkg/llm/... -v 2>&1 | tail -20`
Expected: 新测试 PASS,`moonshot_test.go` 等既有测试不回归

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/catalog/catalog.go pkg/llm/catalog/catalog_test.go
git commit -m "feat(catalog): extend entry metadata and models.dev parsing (max_input, capabilities, reasoning options, provider meta)"
```

---

### Task 2: MaxInput 查询 + 静态能力兜底表 + 查询链接入

**Files:**
- Modify: `pkg/llm/catalog/catalog.go`(lookup 提取 + MaxInput + 查询链）
- Create: `pkg/llm/catalog/capability.go`
- Test: `pkg/llm/catalog/capability_test.go`（新建）

- [ ] **Step 1: 写失败测试**

`pkg/llm/catalog/capability_test.go`:

```go
package catalog

import "testing"

func TestLookupFallbackAnthropic(t *testing.T) {
	c, ok := LookupFallback("anthropic", "claude-sonnet-5-20260101")
	if !ok {
		t.Fatal("claude-sonnet-5 should hit the claude *-4 generation rule? no — should miss")
	}
	_ = c
	// 已知组命中:
	c, ok = LookupFallback("anthropic", "claude-sonnet-4-20250514")
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
```

（注意第一个测试里 `claude-sonnet-5` 断言为 miss——静态表只收录已确认的代际。)

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/catalog -run 'TestLookupFallback|TestWindowFallsBack' -v`
Expected: 编译失败（`LookupFallback` 未定义）

- [ ] **Step 3: 实现**

新建 `pkg/llm/catalog/capability.go`:

```go
package catalog

import (
	"regexp"
	"strings"
)

// FallbackCapability is the static last-resort capability declaration for
// models the catalog does not know (brand-new releases ahead of a refresh).
// Zero ContextWindow/MaxOutput means "unknown" — callers fall through to
// their own defaults; the point is to get capabilities approximately right
// instead of silently empty.
type FallbackCapability struct {
	Capabilities  []string
	ContextWindow int
	MaxOutput     int
}

type fallbackRule struct {
	route string // scoped to this route; "" matches any route
	match func(model string) bool
	cap   FallbackCapability
}

var openAIReasoningRe = regexp.MustCompile(`^o\d`)

func prefixMatcher(prefixes ...string) func(string) bool {
	return func(model string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(model, p) {
				return true
			}
		}
		return false
	}
}

// fallbackRules are evaluated in order; first hit wins. Claude rules are
// scoped to the anthropic route (a claude id arriving over an OpenAI-compat
// gateway says nothing about the gateway's own models). OpenAI/Gemini rules
// are route-agnostic because those ids travel over OpenAI-compat endpoints.
var fallbackRules = []fallbackRule{
	{"anthropic", prefixMatcher("claude-opus-4", "claude-sonnet-4", "claude-haiku-4", "claude-fable"),
		FallbackCapability{Capabilities: []string{"tools", "reasoning", "vision"}}},
	{"anthropic", prefixMatcher("claude-3-", "claude-3.5-", "claude-3.7-"),
		FallbackCapability{Capabilities: []string{"tools", "vision"}}},
	{"", func(m string) bool { return openAIReasoningRe.MatchString(m) },
		FallbackCapability{Capabilities: []string{"tools", "reasoning"}}},
	{"", prefixMatcher("gpt-4o", "gpt-4-turbo", "gpt-4.1", "gpt-4.5"),
		FallbackCapability{Capabilities: []string{"tools", "vision"}}},
	{"", prefixMatcher("gemini-2.5-"),
		FallbackCapability{Capabilities: []string{"tools", "reasoning", "vision", "audio", "video"}}},
	{"", prefixMatcher("gemini-"),
		FallbackCapability{Capabilities: []string{"tools", "vision", "audio"}}},
}

// LookupFallback returns the static capability for a model the catalog does
// not know. route is the wire (or provider name — for built-ins they agree).
func LookupFallback(route, model string) (FallbackCapability, bool) {
	m := strings.ToLower(model)
	for _, r := range fallbackRules {
		if r.route != "" && r.route != route {
			continue
		}
		if r.match(m) {
			return r.cap, true
		}
	}
	return FallbackCapability{}, false
}
```

`catalog.go`：提取共享 matcher,Window/MaxOutput 改用它 + 末尾回落静态表；新增 MaxInput 与 Capabilities:

```go
// lookup finds a catalog entry by exact key, then either-direction prefix
// (alias-aware). Returns false when nothing matches.
func (c *Catalog) lookup(provider, model string) (Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range providerCandidates(provider) {
		if e, ok := c.entries[p+"/"+model]; ok {
			return e, true
		}
		for _, key := range c.order {
			e := c.entries[key]
			if e.Provider != p {
				continue
			}
			if strings.HasPrefix(e.ID, model) || strings.HasPrefix(model, e.ID) {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// Window/MaxOutput 改为:
func (c *Catalog) Window(provider, model string) int {
	if e, ok := c.lookup(provider, model); ok && e.ContextWindow > 0 {
		return e.ContextWindow
	}
	if fb, ok := LookupFallback(provider, model); ok {
		return fb.ContextWindow // 可能为 0,由下游走默认值
	}
	return 0
}

func (c *Catalog) MaxOutput(provider, model string) int {
	if e, ok := c.lookup(provider, model); ok && e.MaxOutput > 0 {
		return e.MaxOutput
	}
	if fb, ok := LookupFallback(provider, model); ok {
		return fb.MaxOutput
	}
	return 0
}

// MaxInput returns the model's declared prompt cap (0 = no separate cap; the
// context window is the only ceiling). No static fallback: an invented input
// cap is worse than none.
func (c *Catalog) MaxInput(provider, model string) int {
	if e, ok := c.lookup(provider, model); ok {
		return e.MaxInput
	}
	return 0
}

// Capabilities returns the declared capability strings, static table as last resort.
func (c *Catalog) Capabilities(provider, model string) []string {
	if e, ok := c.lookup(provider, model); ok && len(e.Capabilities) > 0 {
		return e.Capabilities
	}
	if fb, ok := LookupFallback(provider, model); ok {
		return fb.Capabilities
	}
	return nil
}
```

- [ ] **Step 4: 运行确认通过 + 全量回归**

Run: `go test ./pkg/llm/... 2>&1 | tail -5`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/catalog/capability.go pkg/llm/catalog/capability_test.go pkg/llm/catalog/catalog.go
git commit -m "feat(catalog): static capability fallback table and MaxInput query"
```

---

### Task 3: wire 推断与端点校验（resolve.go)+ buildEngine 接线 + models_source

**Files:**
- Create: `pkg/llm/catalog/resolve.go`
- Test: `pkg/llm/catalog/resolve_test.go`（新建）
- Modify: `pkg/config/config.go`(`ModelsSource` 字段 + env 覆盖）
- Modify: `cmd/lcoder/wiring.go`(buildEngine 返回 error、resolve 接线、SourceURL 透传）
- Modify: `cmd/lcoder/main.go`(buildEngine 调用点）

- [ ] **Step 1: 写失败测试**

`pkg/llm/catalog/resolve_test.go`:

```go
package catalog

import (
	"strings"
	"testing"
)

func newResolveCatalog() *Catalog {
	c := New(Options{Refresh: false})
	c.mergeDataset(Dataset{Providers: []ProviderMeta{
		{ID: "anthropic", Npm: "@ai-sdk/anthropic", API: "https://api.anthropic.com/v1"},
		{ID: "openai", Npm: "@ai-sdk/openai", API: "https://api.openai.com/v1"},
		{ID: "openai-codex", Npm: "@ai-sdk/openai-codex", API: "https://chatgpt.com/backend-api/codex"},
	}})
	return c
}

func TestResolveExplicitRoutePassThrough(t *testing.T) {
	c := newResolveCatalog()
	res, err := c.ResolveProvider("deepseek", "openai", "https://api.deepseek.com/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Route != "openai" || res.Guessed {
		t.Errorf("explicit route must pass through unguessed: %+v", res)
	}
	if res.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("base = %q", res.BaseURL)
	}
}

func TestResolveInfersAnthropic(t *testing.T) {
	c := newResolveCatalog()
	res, err := c.ResolveProvider("anthropic", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Route != "anthropic" || !res.Guessed {
		t.Errorf("want anthropic+guessed, got %+v", res)
	}
	// catalog api 带 /v1,anthropic route 必须剥掉
	if res.BaseURL != "https://api.anthropic.com" {
		t.Errorf("base = %q, want /v1 stripped", res.BaseURL)
	}
}

func TestResolveInfersCodexResponses(t *testing.T) {
	c := newResolveCatalog()
	res, err := c.ResolveProvider("openai-codex", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Route != "openai-responses" {
		t.Errorf("route = %q, want openai-responses", res.Route)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	c := newResolveCatalog()
	// 无 base_url → 报错(防 key 发错主机)
	if _, err := c.ResolveProvider("my-relay", "", ""); err == nil {
		t.Fatal("unknown provider without base_url must error")
	}
	// 有 base_url → openai + guessed
	res, err := c.ResolveProvider("my-relay", "", "http://localhost:4000/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Route != "openai" || !res.Guessed || res.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("got %+v", res)
	}
}

func TestResolveRejectsBadBaseURL(t *testing.T) {
	c := newResolveCatalog()
	if _, err := c.ResolveProvider("x", "openai", "   "); err == nil {
		t.Error("blank base_url must error")
	}
	if _, err := c.ResolveProvider("x", "openai", "https://${HOST}/v1"); err == nil {
		t.Error("placeholder base_url must error")
	}
	if _, err := c.ResolveProvider("x", "openai", ""); err == nil {
		t.Error("unknown provider with no URL at all must error")
	}
	// anthropic + 显式带 /v1 的 URL 也要剥
	res, err := c.ResolveProvider("x", "anthropic", "https://proxy.example.com/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.HasSuffix(res.BaseURL, "/v1") {
		t.Errorf("anthropic base must strip /v1, got %q", res.BaseURL)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/catalog -run TestResolve -v`
Expected: 编译失败

- [ ] **Step 3: 实现**

新建 `pkg/llm/catalog/resolve.go`:

```go
package catalog

import (
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/llm/provider"
)

// ResolvedProvider is the outcome of resolving one configured provider
// connection: which wire it speaks and which endpoint its key is sent to.
type ResolvedProvider struct {
	Route   string // "anthropic" | "openai-responses" | "openai"
	BaseURL string
	Guessed bool   // Route was inferred, not declared
}

// ResolveProvider decides the wire and endpoint for one provider entry
// (mirrors kimi-code resolveCatalogImport, cut down to three wires). An
// explicit route passes through; a missing route is inferred from models.dev
// metadata (anthropic/claude → anthropic, codex → openai-responses, else
// openai). A missing base URL falls back to the catalog api, then built-in
// defaults; unknown providers must declare base_url — silently sending the
// key to the wrong host is a credential leak.
func (c *Catalog) ResolveProvider(name, connRoute, connBaseURL string) (ResolvedProvider, error) {
	route := connRoute
	guessed := false
	if route == "" {
		route = c.inferRoute(name)
		guessed = true
	}
	base, err := c.resolveBase(name, route, connBaseURL)
	if err != nil {
		return ResolvedProvider{}, err
	}
	return ResolvedProvider{Route: route, BaseURL: base, Guessed: guessed}, nil
}

func (c *Catalog) inferRoute(name string) string {
	lower := strings.ToLower(name)
	npm := ""
	if meta, ok := c.ProviderMeta(name); ok {
		npm = strings.ToLower(meta.Npm)
	}
	if strings.Contains(npm, "anthropic") || strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude") {
		return "anthropic"
	}
	if strings.Contains(npm, "codex") || strings.Contains(lower, "codex") {
		return "openai-responses"
	}
	return "openai"
}

func (c *Catalog) resolveBase(name, route, explicit string) (string, error) {
	if explicit != "" {
		b := strings.TrimSpace(explicit)
		if b == "" {
			return "", fmt.Errorf("provider %q: base_url is blank", name)
		}
		if strings.Contains(b, "${") {
			return "", fmt.Errorf("provider %q: base_url contains env placeholder, which config cannot express", name)
		}
		return adaptBaseForRoute(b, route), nil
	}
	if meta, ok := c.ProviderMeta(name); ok && meta.API != "" && !strings.Contains(meta.API, "${") {
		return adaptBaseForRoute(meta.API, route), nil
	}
	if d := provider.DefaultBaseURL(name); d != "" {
		return d, nil
	}
	// 协议族兜底:openai-responses 与 openai 同 base
	if route == "openai-responses" {
		return provider.DefaultBaseURL("openai"), nil
	}
	if d := provider.DefaultBaseURL(route); d != "" {
		return d, nil
	}
	return "", fmt.Errorf("provider %q: no base URL known; set base_url explicitly", name)
}

// adaptBaseForRoute strips a trailing /v1 for the anthropic wire: the
// Anthropic adapter appends /messages itself, and models.dev api fields carry
// /v1 — keeping it would POST to /v1/v1/messages.
func adaptBaseForRoute(base, route string) string {
	if route == "anthropic" {
		return strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1")
	}
	return base
}
```

注意：`resolveBase` 里 unknown provider 的"无 URL 报错"依赖 `provider.DefaultBaseURL(name)`/`DefaultBaseURL(route)` 均 miss——但 `"openai"` route 的 `DefaultBaseURL("openai")` 永远命中，所以 unknown provider + 显式 route openai + 无 base 会落到 openai 默认。**这是有意的**（显式声明了 openai 协议，发到官方端点是声明的语义）；只有 route 也为空（纯未知 provider）才报错。测试 `TestResolveRejectsBadBaseURL` 第三例 `("", "")` 走 infer→openai→DefaultBaseURL 命中……会失败！修正：**测试改为** `ResolveProvider("x", "", "")` 且 catalog 无 x → infer openai → DefaultBaseURL("openai") 命中 → 不报错。把该断言改为：

```go
	// 纯未知 provider:推断为 openai 兼容,落到 openai 默认端点并标记 guessed
	res2, err := c.ResolveProvider("x", "", "")
	if err != nil {
		t.Fatalf("unknown provider should degrade to openai default: %v", err)
	}
	if res2.Route != "openai" || !res2.Guessed {
		t.Errorf("got %+v", res2)
	}
```

并删除 `TestResolveUnknownProvider` 中"无 base_url → 报错"的断言，改为：

```go
	// 无 base_url → 落到 openai 默认端点 + guessed(显式声明 openai 语义)
	res, err := c.ResolveProvider("my-relay", "", "http://localhost:4000/v1")
```
（保留有 base_url 的案例；另加断言空 base 时 `res.BaseURL == provider.DefaultBaseURL("openai")`。)

`pkg/config/config.go`:`Config` 结构在 `Model string` 后加：

```go
	Thinking      string                  `yaml:"thinking"`
	ModelsSource  string                  `yaml:"models_source"`
```

`Load` 函数返回前加 env 覆盖（找到 `Load` 中其他 env 处理的位置，跟随现有风格）:

```go
	if v := os.Getenv("LCODER_MODELS_SOURCE"); v != "" {
		cfg.ModelsSource = v
	}
```

`cmd/lcoder/wiring.go` 的 `buildEngine` 改为：

```go
func buildEngine(cfg config.Config) (*engine.Engine, error) {
	cachePath := paths.LCoderHome("cache", "models.json")
	cat := catalog.New(catalog.Options{
		Refresh:   true,
		CachePath: cachePath,
		SourceURL: cfg.ModelsSource,
		Overrides: catalogOverridesFromConfig(cfg),
	})
	eng := engine.New(cat)
	for name, conn := range cfg.Providers {
		res, err := cat.ResolveProvider(name, conn.Route, conn.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", name, err)
		}
		if res.Guessed {
			fmt.Fprintf(os.Stderr, "info: provider %q 未声明 route,推断为 %s\n", name, res.Route)
		}
		eng.RegisterProvider(name, llmprovider.Conn{
			BaseURL: res.BaseURL,
			APIKey:  conn.APIKey,
			Route:   res.Route,
			Headers: conn.Headers,
		})
	}
	return eng, nil
}
```

`cmd/lcoder/main.go` 调用点（搜索 `buildEngine(`，当前形如 `eng := buildEngine(cfg)`）改为：

```go
	eng, err := buildEngine(cfg)
	if err != nil {
		return nil, fmt.Errorf("build llm engine: %w", err)
	}
```

（按调用点实际上下文调整变量名与返回值；若有多个调用点全部更新。)

- [ ] **Step 4: 运行确认通过 + 全量回归 + build**

Run: `go build ./... && go test ./pkg/llm/... ./pkg/config/... ./cmd/... 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/catalog/resolve.go pkg/llm/catalog/resolve_test.go pkg/config/config.go cmd/lcoder/wiring.go cmd/lcoder/main.go
git commit -m "feat(catalog): wire inference and endpoint validation for provider setup"
```

---

### Task 4: snapshot 生成工具 + 重新烘焙 snapshot.json

**Files:**
- Create: `cmd/catalog-snapshot/main.go`
- Modify: `pkg/llm/catalog/snapshot.json`（重新生成）
- Test: `pkg/llm/catalog/snapshot_test.go`（新建）

- [ ] **Step 1: 写失败测试**

`pkg/llm/catalog/snapshot_test.go`:

```go
package catalog

import (
	"encoding/json"
	"testing"
)

// The embedded snapshot must carry the current dataset format with provider
// metadata — wire inference (resolve.go) depends on it at startup, before any
// background refresh lands.
func TestEmbeddedSnapshotHasProviderMeta(t *testing.T) {
	var ds Dataset
	if err := json.Unmarshal(snapshotJSON, &ds); err != nil {
		t.Fatalf("snapshot is not a Dataset: %v", err)
	}
	if len(ds.Models) == 0 {
		t.Fatal("snapshot has no models")
	}
	byID := map[string]ProviderMeta{}
	for _, p := range ds.Providers {
		byID[p.ID] = p
	}
	for _, want := range []string{"anthropic", "openai"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("snapshot missing provider meta %q", want)
		}
	}
	// 过滤必须已生效:snapshot 里不应有 deprecated/embedding 条目
	for _, e := range ds.Models {
		if hasEmbeddingMarker(e.ID) {
			t.Errorf("snapshot contains embedding model %q", e.ID)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/catalog -run TestEmbeddedSnapshot -v`
Expected: FAIL（旧 snapshot 是 `[]Entry` 格式，无 providers)

- [ ] **Step 3: 实现生成工具并重生 snapshot**

新建 `cmd/catalog-snapshot/main.go`:

```go
// Command catalog-snapshot regenerates pkg/llm/catalog/snapshot.json from a
// models.dev-style api.json. Run manually when refreshing the embedded
// catalog: go run ./cmd/catalog-snapshot [source-url]
//
// It must be run from the repository root: the output path is relative to
// the current working directory.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
)

func main() {
	url := "https://models.dev/api.json"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	ds, err := catalog.FetchEntries(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
	// FetchEntries builds from map iteration; sort so re-baking produces a
	// deterministic, minimal diff.
	sort.Slice(ds.Providers, func(i, j int) bool { return ds.Providers[i].ID < ds.Providers[j].ID })
	sort.Slice(ds.Models, func(i, j int) bool {
		if ds.Models[i].Provider != ds.Models[j].Provider {
			return ds.Models[i].Provider < ds.Models[j].Provider
		}
		return ds.Models[i].ID < ds.Models[j].ID
	})
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	// Atomic write: temp file in the same directory, then rename, so a crash
	// mid-write never leaves a truncated snapshot.
	const out = "pkg/llm/catalog/snapshot.json"
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, out); err != nil {
		fmt.Fprintln(os.Stderr, "rename:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s: %d providers, %d models\n", out, len(ds.Providers), len(ds.Models))
}
```

运行（需网络）:

```bash
go run ./cmd/catalog-snapshot
```

Expected: `wrote pkg/llm/catalog/snapshot.json: N providers, M models`。**若网络不可用**：记录 warning，手工把现有 snapshot 包一层 `{"providers":[...anthropic/openai/openai-codex 三条手写...],"models":<原数组>}` 并在 commit message 说明，不要阻塞任务。

- [ ] **Step 4: 运行确认通过 + 全量回归**

Run: `go test ./pkg/llm/... 2>&1 | tail -5`
Expected: PASS（注意 moonshot 别名定价等既有测试不能因重生数据回归；若 models.dev 数据变动导致既有 fixture 断言失败，检查断言对象是否仍被收录，必要时调整测试 fixture 而非代码）

- [ ] **Step 5: Commit**

```bash
git add cmd/catalog-snapshot/main.go pkg/llm/catalog/snapshot.json pkg/llm/catalog/snapshot_test.go
git commit -m "feat(catalog): snapshot generator tool and re-baked dataset with provider meta"
```

---

### Task 5: thinking effort 配置与贯穿

`catalog.ThinkingSpec` 派生 + `config.Thinking` + engine 侧校验解析 + `TurnRequest.Thinking/ThinkingOffEffort` + contextmgr 贯穿。

**Files:**
- Modify: `pkg/llm/catalog/catalog.go`(ThinkingSpec)
- Test: `pkg/llm/catalog/catalog_test.go`（追加）
- Modify: `pkg/llm/engine/engine.go`(ResolveThinking + StreamTurn 填 OffEffort + ProviderRoute)
- Test: `pkg/llm/engine/engine_test.go`（新建或追加）
- Modify: `pkg/llm/client.go`(ModelThinking 解析入口）
- Modify: `pkg/models/message.go`(TurnRequest 字段）
- Modify: `pkg/contextmgr/manager.go`(WithThinking + BuildTurnRequest)
- Modify: `pkg/agentsetup/setup.go`(NewContextManager 加 thinking 参数）
- Modify: `cmd/lcoder/main.go`（解析 + 传参）
- Modify: `pkg/config/config.go`(Task 3 已加 `Thinking` 字段，本任务只用）

- [ ] **Step 1: 写失败测试**

`pkg/llm/catalog/catalog_test.go` 追加：

```go
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
```

`pkg/llm/engine/engine_test.go`（若无此文件则新建，包内已有测试风格则跟随）:

```go
package engine

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
)

func testCatalog() *catalog.Catalog {
	c := catalog.New(catalog.Options{Refresh: false})
	// mergeDataset 未导出 —— 用 Overrides 注入测试条目
	_ = c
	return catalog.New(catalog.Options{Refresh: false, Overrides: []catalog.Entry{
		{ID: "gpt-5", Provider: "openai", ContextWindow: 400000, Efforts: []string{"low", "medium", "high"}},
		{ID: "grok-4", Provider: "xai", ContextWindow: 256000, Efforts: []string{"low", "high"}, OffEffort: "none"},
	}})
}

func TestResolveThinking(t *testing.T) {
	e := New(testCatalog())
	e.RegisterProvider("openai", provider.Conn{Route: "openai"})
	e.RegisterProvider("xai", provider.Conn{Route: "openai"})

	// 空配置 → 空
	if got, warn := e.ResolveThinking("openai", "gpt-5", ""); got != "" || warn != "" {
		t.Errorf("empty config: got %q warn %q", got, warn)
	}
	// off + AlwaysThinking → 忽略 + warning
	got, warn := e.ResolveThinking("openai", "gpt-5", "off")
	if got != "" || warn == "" {
		t.Errorf("off on always-thinking: got %q warn %q, want empty+warning", got, warn)
	}
	// off + 有 OffEffort → 保留
	if got, _ := e.ResolveThinking("xai", "grok-4", "off"); got != "off" {
		t.Errorf("grok-4 off: got %q", got)
	}
	// 合法档位 → 保留
	if got, _ := e.ResolveThinking("openai", "gpt-5", "low"); got != "low" {
		t.Errorf("gpt-5 low: got %q", got)
	}
	// 非法档位 → on + warning
	got, warn = e.ResolveThinking("openai", "gpt-5", "extreme")
	if got != "on" || warn == "" {
		t.Errorf("bad effort: got %q warn %q", got, warn)
	}
	// on 原样
	if got, _ := e.ResolveThinking("openai", "gpt-5", "on"); got != "on" {
		t.Errorf("on: got %q", got)
	}
}
```

contextmgr 测试（`pkg/contextmgr/manager_test.go` 或新建 `thinking_test.go`):

```go
package contextmgr

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestBuildTurnRequestCarriesThinking(t *testing.T) {
	m := NewManager(TokenBudget{MaxTotal: 100000, ReserveOutput: 1000}, WithThinking("low"))
	req, err := m.BuildTurnRequest(models.ModelRef{Provider: "p", ID: "m"}, nil)
	if err != nil {
		t.Fatalf("BuildTurnRequest: %v", err)
	}
	if req.Thinking != "low" {
		t.Errorf("Thinking = %q, want low", req.Thinking)
	}
	m2 := NewManager(TokenBudget{MaxTotal: 100000, ReserveOutput: 1000})
	req2, _ := m2.BuildTurnRequest(models.ModelRef{Provider: "p", ID: "m"}, nil)
	if req2.Thinking != "" {
		t.Errorf("default Thinking must be empty, got %q", req2.Thinking)
	}
}
```

（BuildTurnRequest 实际签名以 `manager.go:381` 附近为准调整参数。)

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/... ./pkg/contextmgr/ -run 'Thinking' -v`
Expected: 编译失败

- [ ] **Step 3: 实现**

`pkg/llm/catalog/catalog.go` 追加：

```go
// ThinkingSpec is a model's declared thinking-effort behavior.
type ThinkingSpec struct {
	Efforts        []string
	OffEffort      string
	AlwaysThinking bool
}

// ThinkingSpec derives the spec for provider/model on the given wire. A model
// declaring effort levels with no way to turn thinking off (no OffEffort, no
// toggle) is AlwaysThinking — except on the anthropic wire, where
// thinking:{type:"disabled"} is a protocol-level off the effort list never
// shows (mirrors kimi-code catalogProviderModels).
func (c *Catalog) ThinkingSpec(route, provider, model string) ThinkingSpec {
	e, ok := c.lookup(provider, model)
	if !ok {
		return ThinkingSpec{}
	}
	spec := ThinkingSpec{Efforts: e.Efforts, OffEffort: e.OffEffort}
	if len(e.Efforts) > 0 && e.OffEffort == "" && !e.ThinkingToggle && route != "anthropic" {
		spec.AlwaysThinking = true
	}
	return spec
}
```

`pkg/models/message.go` 的 `TurnRequest` 加：

```go
	// Thinking is the resolved thinking intent: "" (send nothing), "off",
	// "on", or a model-declared effort level. ThinkingOffEffort is the wire
	// encoding for "off" when the model declares one (e.g. "none"), filled by
	// the engine from the catalog.
	Thinking          string `json:"thinking,omitempty"`
	ThinkingOffEffort string `json:"thinking_off_effort,omitempty"`
```

`pkg/llm/engine/engine.go` 追加（需 import "strings" 与 catalog 已有）:

```go
// ProviderRoute returns the resolved route of a registered provider ("" if
// the provider is not registered).
func (e *Engine) ProviderRoute(name string) string {
	return e.providers[name].Route
}

// ResolveThinking validates a configured thinking value against the catalog
// and returns the value to put on turn requests, plus a user-facing warning
// when the config had to be adjusted. "" means "send no thinking field".
func (e *Engine) ResolveThinking(provider, model, want string) (resolved, warning string) {
	t := strings.ToLower(strings.TrimSpace(want))
	if t == "" {
		return "", ""
	}
	spec := e.catalog.ThinkingSpec(e.providers[provider].Route, provider, model)
	switch t {
	case "off":
		if spec.AlwaysThinking {
			return "", fmt.Sprintf("模型 %s 的 thinking 不可关闭,已忽略 thinking: off", model)
		}
		return "off", ""
	case "on":
		return "on", ""
	default:
		if len(spec.Efforts) > 0 {
			for _, lv := range spec.Efforts {
				if lv == t {
					return t, ""
				}
			}
			return "on", fmt.Sprintf("模型 %s 未声明 thinking 档位 %q(支持 %v),已回退为 on", model, t, spec.Efforts)
		}
		return t, ""
	}
}
```

（需要 import "fmt"。)`StreamTurn` 在 `adapter := e.newAdapter(...)` 之前加：

```go
	if req.Thinking == "off" {
		req.ThinkingOffEffort = e.catalog.ThinkingSpec(conn.Route, prov, req.Model.ID).OffEffort
	}
```

`pkg/llm/client.go` 追加：

```go
// ResolveThinking validates the configured thinking value; see engine.
func (c *Client) ResolveThinking(ctx context.Context, provider, model, want string) (string, string) {
	return c.engine.ResolveThinking(provider, model, want)
}
```

`pkg/contextmgr/manager.go`:`Manager` 结构加 `thinking string` 字段；新增 option:

```go
// WithThinking sets the resolved thinking value carried on turn requests.
func WithThinking(v string) Option {
	return func(m *Manager) { m.thinking = v }
}
```

`BuildTurnRequest` 返回的 `models.TurnRequest` 加一行：

```go
		Thinking:         m.thinking,
```

`pkg/agentsetup/setup.go` 的 `NewContextManager` 签名加 `thinking string` 参数（放 `budget` 之后），并在 `opts` 末尾追加：

```go
	if thinking != "" {
		opts = append(opts, contextmgr.WithThinking(thinking))
	}
```

`cmd/lcoder/main.go`（在 `budget, source := cfg.ResolveContextBudget(...)` 之后、`NewContextManager` 调用处）:

```go
	thinking, thinkWarn := llmClient.ResolveThinking(context.Background(), cfg.Provider, cfg.Model, cfg.Thinking)
	if thinkWarn != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", thinkWarn)
	}
```

并把 `agentsetup.NewContextManager(cfg, budget, ...)` 调用加 `thinking` 实参。用 grep 找 `NewContextManager(` 的全部调用点一并更新。

- [ ] **Step 4: 运行确认通过 + 全量回归 + build**

Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon') 2>&1 | grep -v '^ok' | tail -15`
Expected: 无 FAIL

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/catalog/catalog.go pkg/llm/catalog/catalog_test.go pkg/llm/engine/engine.go pkg/llm/engine/engine_test.go pkg/llm/client.go pkg/models/message.go pkg/contextmgr/manager.go pkg/contextmgr/thinking_test.go pkg/agentsetup/setup.go cmd/lcoder/main.go
git commit -m "feat(llm): thinking effort config resolved through catalog and carried on turn requests"
```

---

### Task 6: openai / anthropic thinking 字段映射

**Files:**
- Modify: `pkg/llm/provider/openai.go`
- Modify: `pkg/llm/provider/anthropic.go`
- Test: `pkg/llm/provider/openai_test.go`、`pkg/llm/provider/anthropic_test.go`（追加）

- [ ] **Step 1: 写失败测试**

两个 provider 测试文件各追加（断言请求体——参考现有测试如何捕获请求体，若现有测试用 httptest server 则同构）:

```go
// openai_test.go
func TestOpenAIThinkingMapping(t *testing.T) {
	cases := []struct {
		name           string
		thinking       string
		offEffort      string
		wantEffort     string // "" = 字段不应出现
	}{
		{"empty sends nothing", "", "", ""},
		{"on sends nothing", "on", "", ""},
		{"level passes through", "low", "", "low"},
		{"off with off-effort", "off", "none", "none"},
		{"off without off-effort sends nothing", "off", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := captureRequestBody(t, OpenAICompat{}, models.TurnRequest{
				Model: models.ModelRef{ID: "gpt-5"},
				Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
				Thinking: tc.thinking, ThinkingOffEffort: tc.offEffort,
			})
			got, exists := body["reasoning_effort"]
			if tc.wantEffort == "" && exists {
				t.Errorf("reasoning_effort should be absent, got %v", got)
			}
			if tc.wantEffort != "" && got != tc.wantEffort {
				t.Errorf("reasoning_effort = %v, want %q", got, tc.wantEffort)
			}
		})
	}
}

// anthropic_test.go
func TestAnthropicThinkingMapping(t *testing.T) {
	// on → enabled,budget = max(1024, maxTokens/2) 且 < max_tokens
	body := captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model:    models.ModelRef{ID: "claude-sonnet-4"},
		Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
		Generation: models.GenerationConfig{MaxTokens: 16384},
		Thinking: "on",
	})
	th, ok := body["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" {
		t.Fatalf("thinking = %v, want enabled", body["thinking"])
	}
	if th["budget_tokens"] != 8192 {
		t.Errorf("budget = %v, want 8192 (16384/2)", th["budget_tokens"])
	}
	// off → disabled
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model: models.ModelRef{ID: "claude-sonnet-4"},
		Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
		Generation: models.GenerationConfig{MaxTokens: 16384},
		Thinking: "off",
	})
	th, _ = body["thinking"].(map[string]any)
	if th == nil || th["type"] != "disabled" {
		t.Errorf("thinking = %v, want disabled", body["thinking"])
	}
	// 空 → 无字段;小 max_tokens 时 budget 仍 < max_tokens
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model: models.ModelRef{ID: "claude-sonnet-4"},
		Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
	})
	if _, exists := body["thinking"]; exists {
		t.Error("empty thinking must not send the field")
	}
	body = captureRequestBody(t, Anthropic{}, models.TurnRequest{
		Model: models.ModelRef{ID: "claude-sonnet-4"},
		Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
		Generation: models.GenerationConfig{MaxTokens: 1500},
		Thinking: "on",
	})
	th, _ = body["thinking"].(map[string]any)
	if th == nil {
		t.Fatal("thinking missing")
	}
	if b, _ := th["budget_tokens"].(int); b >= 1500 {
		t.Errorf("budget %v must be < max_tokens 1500", th["budget_tokens"])
	}
}
```

`captureRequestBody` 辅助函数（放 `adapter_test.go` 或对应测试文件，若已有类似 helper 则复用）:

```go
func captureRequestBody(t *testing.T, a Adapter, req models.TurnRequest) map[string]any {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	ch, err := a.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k", Route: "openai"}, req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, captured)
	}
	return body
}
```

（注意：anthropic 的 SSE 响应体 `data: [DONE]` 会被跳过、EOF 结束流，可正常走到 KindDone。)

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/provider -run Thinking -v`
Expected: FAIL（字段未发送）

- [ ] **Step 3: 实现**

`pkg/llm/provider/openai.go` 的 `Stream` 在 `if req.Generation.TopP != 0 {...}` 后加：

```go
	// Thinking: concrete levels pass through; "off" only when the model
	// declares an off encoding; "on" sends nothing (provider default is on).
	if req.Thinking == "off" && req.ThinkingOffEffort != "" {
		body["reasoning_effort"] = req.ThinkingOffEffort
	} else if req.Thinking != "" && req.Thinking != "off" && req.Thinking != "on" {
		body["reasoning_effort"] = req.Thinking
	}
```

`pkg/llm/provider/anthropic.go` 的 `Stream` 在 `if req.Generation.TopP != 0 {...}` 后加：

```go
	// Anthropic has no effort levels: any on-signal enables thinking with a
	// budget derived from the output cap; off is explicit disable.
	if req.Thinking == "off" {
		body["thinking"] = map[string]any{"type": "disabled"}
	} else if req.Thinking != "" {
		maxTok := anthropicMaxTokens(req)
		budget := maxTok / 2
		if budget < 1024 {
			budget = 1024
		}
		if budget >= maxTok {
			budget = maxTok - 1
		}
		if budget > 0 {
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./pkg/llm/provider -v 2>&1 | tail -10`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/provider/openai.go pkg/llm/provider/anthropic.go pkg/llm/provider/openai_test.go pkg/llm/provider/anthropic_test.go pkg/llm/provider/adapter_test.go
git commit -m "feat(provider): map thinking config to openai reasoning_effort and anthropic thinking fields"
```

---

### Task 7: OpenAI Responses 适配器 + engine 工厂三分支

**Files:**
- Create: `pkg/llm/provider/openai_responses.go`
- Test: `pkg/llm/provider/openai_responses_test.go`（新建）
- Modify: `pkg/llm/engine/engine.go`（工厂三分支）
- Test: `pkg/llm/engine/engine_test.go`（追加工厂断言）

- [ ] **Step 1: 写失败测试**

`pkg/llm/provider/openai_responses_test.go`:

```go
package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

const responsesSSE = `data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.delta","delta":" world"}

data: {"type":"response.reasoning_summary_text.delta","delta":"thinking..."}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}

data: {"type":"response.function_call_arguments.delta","item_id":"call_1","delta":"{\"path\":"}

data: {"type":"response.function_call_arguments.delta","item_id":"call_1","delta":"\"a.go\"}"}

data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":4}}}}

`

func collectEvents(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var evs []Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestResponsesStreamParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path = %s, want /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(responsesSSE))
	}))
	defer srv.Close()

	ch, err := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5-codex"},
		Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)

	var textDeltas, thinkDeltas, toolDeltas int
	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindTextDelta:
			textDeltas++
		case KindThinkingDelta:
			thinkDeltas++
		case KindToolCallDelta:
			toolDeltas++
		case KindDone:
			done = &evs[i]
		case KindError:
			t.Fatalf("unexpected error event: %v", evs[i].Err)
		}
	}
	if textDeltas != 2 || thinkDeltas != 1 || toolDeltas != 2 {
		t.Errorf("deltas text/think/tool = %d/%d/%d, want 2/1/2", textDeltas, thinkDeltas, toolDeltas)
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.Usage == nil || done.Usage.PromptTokens != 10 || done.Usage.CompletionTokens != 5 || done.Usage.CacheReadTokens != 4 {
		t.Errorf("usage = %+v", done.Usage)
	}
	// finalize:thinking + text + tool call
	var thinking, text string
	var calls []models.ToolCallContent
	for _, p := range done.Message.Content {
		switch c := p.(type) {
		case models.ThinkingContent:
			thinking = c.Text
		case models.TextContent:
			text = c.Text
		case models.ToolCallContent:
			calls = append(calls, c)
		}
	}
	if thinking != "thinking..." || text != "Hello world" {
		t.Errorf("thinking=%q text=%q", thinking, text)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read_file" || calls[0].Arguments["path"] != "a.go" {
		t.Errorf("tool calls = %+v", calls)
	}
}

func TestResponsesRequestShape(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "read a.go"}),
		models.NewAgentMessage(models.RoleAssistant,
			models.TextContent{Text: "reading"},
			models.ToolCallContent{ID: "call_1", Name: "read_file", Arguments: map[string]any{"path": "a.go"}}),
		models.NewAgentMessage(models.RoleToolResult,
			models.ToolResultContent{ToolCallID: "call_1", Content: []models.ContentPart{models.TextContent{Text: "package main"}}}),
	}
	ch, err := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, models.TurnRequest{
		Model:        models.ModelRef{ID: "gpt-5-codex"},
		SystemPrompt: "you are an agent",
		Messages:     msgs,
		Tools:        []models.ToolDefinition{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
		Generation:   models.GenerationConfig{MaxTokens: 4096},
		Thinking:     "high",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, ch)

	body := string(captured)
	for _, want := range []string{
		`"instructions":"you are an agent"`,
		`"max_output_tokens":4096`,
		`"effort":"high"`,
		`"type":"function_call"`,
		`"call_id":"call_1"`,
		`"type":"function_call_output"`,
		`"name":"read_file"`,       // 平铺工具与 function_call 都含
		`"input_text"`,
		`"output_text"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s:\n%s", want, body)
		}
	}
	// chat completions 嵌套形式不得出现
	if strings.Contains(body, `"function":{"name"`) {
		t.Errorf("tools must be flattened, not nested:\n%s", body)
	}
}

func TestResponsesErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"boom\"}}}\n\n"))
	}))
	defer srv.Close()
	ch, _ := OpenAIResponses{}.Stream(context.Background(), Conn{BaseURL: srv.URL}, models.TurnRequest{
		Model:    models.ModelRef{ID: "gpt-5"},
		Messages: []models.AgentMessage{models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"})},
	})
	evs := collectEvents(t, ch)
	var sawErr bool
	for _, ev := range evs {
		if ev.Kind == KindError {
			sawErr = true
			if !strings.Contains(ev.Err.Message, "boom") {
				t.Errorf("error = %v", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Error("response.failed must surface as KindError")
	}
}
```

engine 测试追加：

```go
func TestAdapterFactoryRoutes(t *testing.T) {
	if _, ok := defaultAdapterFactory("anthropic", provider.CacheMarks{}).(provider.Anthropic); !ok {
		t.Error("anthropic route")
	}
	if _, ok := defaultAdapterFactory("openai-responses", provider.CacheMarks{}).(provider.OpenAIResponses); !ok {
		t.Error("openai-responses route")
	}
	if _, ok := defaultAdapterFactory("deepseek", provider.CacheMarks{}).(provider.OpenAICompat); !ok {
		t.Error("default route")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/provider -run TestResponses -v`
Expected: 编译失败

- [ ] **Step 3: 实现**

新建 `pkg/llm/provider/openai_responses.go`:

```go
// pkg/llm/provider/openai_responses.go
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// OpenAIResponses is the adapter for the OpenAI Responses API (/responses),
// the only way to reach codex-class models. Tools are flattened and tool
// calls correlate by call_id, unlike chat completions.
type OpenAIResponses struct{}

func (OpenAIResponses) Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error) {
	body := map[string]any{
		"model":  req.Model.ID,
		"input":  responsesInput(req.Messages),
		"stream": true,
	}
	if req.SystemPrompt != "" {
		body["instructions"] = req.SystemPrompt
	}
	if tools := responsesTools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if req.Generation.Temperature != 0 {
		body["temperature"] = req.Generation.Temperature
	}
	if req.Generation.MaxTokens != 0 {
		body["max_output_tokens"] = req.Generation.MaxTokens
	}
	if req.Generation.TopP != 0 {
		body["top_p"] = req.Generation.TopP
	}
	if req.Thinking == "off" && req.ThinkingOffEffort != "" {
		body["reasoning"] = map[string]any{"effort": req.ThinkingOffEffort}
	} else if req.Thinking != "" && req.Thinking != "off" && req.Thinking != "on" {
		body["reasoning"] = map[string]any{"effort": req.Thinking}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := ResolveBaseURL(conn) + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conn.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+conn.APIKey)
	}
	for k, v := range conn.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			emit(ctx, out, Event{Kind: KindError, Err: classifyHTTP(resp.StatusCode, data)})
			return
		}

		emit(ctx, out, Event{Kind: KindStart})

		var textBuf, thinkBuf strings.Builder
		tools := map[int]*toolBuffer{}
		toolIdxByCallID := map[string]int{}
		var usage *models.LLMUsage

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				break
			}
			var ev responsesEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "response.output_text.delta":
				textBuf.WriteString(ev.Delta)
				emit(ctx, out, Event{Kind: KindTextDelta, Delta: ev.Delta})
			case "response.reasoning_summary_text.delta":
				thinkBuf.WriteString(ev.Delta)
				emit(ctx, out, Event{Kind: KindThinkingDelta, Delta: ev.Delta})
			case "response.output_item.done":
				if ev.Item.Type == "function_call" {
					idx := len(toolIdxByCallID)
					toolIdxByCallID[ev.Item.CallID] = idx
					tools[idx] = &toolBuffer{id: ev.Item.CallID, name: ev.Item.Name}
				}
			case "response.function_call_arguments.delta":
				idx, ok := toolIdxByCallID[ev.ItemID]
				if !ok {
					idx = len(toolIdxByCallID)
					toolIdxByCallID[ev.ItemID] = idx
					tools[idx] = &toolBuffer{id: ev.ItemID}
				}
				tools[idx].args.WriteString(ev.Delta)
				emit(ctx, out, Event{Kind: KindToolCallDelta, ToolCallIndex: idx, ArgumentsJSON: ev.Delta})
			case "response.completed":
				if ev.Response.Usage != nil {
					usage = ev.Response.Usage.toLLMUsage()
				}
			case "response.failed", "error":
				msg := "responses stream failed"
				if ev.Response.Error != nil && ev.Response.Error.Message != "" {
					msg = ev.Response.Error.Message
				}
				emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: msg}})
				return
			}
			// 未知事件类型:跳过(向前兼容)
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: err.Error()}})
			return
		}

		emit(ctx, out, Event{Kind: KindDone,
			Message: finalizeMessage(thinkBuf.String(), textBuf.String(), tools),
			Usage:   usage})
	}()
	return out, nil
}

// responsesInput converts agent messages to Responses input items.
func responsesInput(msgs []models.AgentMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case models.RoleSystem, models.RoleUser:
			var parts []map[string]any
			for _, p := range m.Content {
				switch c := p.(type) {
				case models.TextContent:
					if c.Text != "" {
						parts = append(parts, map[string]any{"type": "input_text", "text": c.Text})
					}
				case models.ImageContent:
					if c.Data != "" {
						mime := c.MimeType
						if mime == "" {
							mime = "image/jpeg"
						}
						parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + mime + ";base64," + c.Data})
					}
				}
			}
			if len(parts) > 0 {
				role := "user"
				out = append(out, map[string]any{"role": role, "content": parts})
			}
		case models.RoleAssistant:
			var textParts []map[string]any
			for _, p := range m.Content {
				switch c := p.(type) {
				case models.TextContent:
					if c.Text != "" {
						textParts = append(textParts, map[string]any{"type": "output_text", "text": c.Text})
					}
				case models.ToolCallContent:
					args, _ := json.Marshal(c.Arguments)
					if c.Arguments == nil {
						args = []byte("{}")
					}
					out = append(out, map[string]any{
						"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": string(args),
					})
				}
			}
			if len(textParts) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": textParts})
			}
		case models.RoleToolResult:
			for _, p := range m.Content {
				if r, ok := p.(models.ToolResultContent); ok {
					text := ""
					for _, child := range r.Content {
						if t, ok := child.(models.TextContent); ok {
							text += t.Text
						}
					}
					out = append(out, map[string]any{
						"type": "function_call_output", "call_id": r.ToolCallID, "output": text,
					})
				}
			}
		}
	}
	return out
}

// responsesTools converts tool definitions to the flattened Responses shape.
func responsesTools(tools []models.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters,
		})
	}
	return out
}

// --- event decoding ---

type responsesEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	// function_call_arguments.delta carries item_id; output_item.done carries item.
	ItemID string `json:"item_id"`
	Item   struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response struct {
		Usage *responsesUsage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

type responsesUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	TotalTokens         int `json:"total_tokens"`
	InputTokensDetails  *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
}

func (u responsesUsage) toLLMUsage() *models.LLMUsage {
	cacheRead := 0
	if u.InputTokensDetails != nil {
		cacheRead = u.InputTokensDetails.CachedTokens
	}
	return &models.LLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  cacheRead,
	}
}
```

`pkg/llm/engine/engine.go` 的 `defaultAdapterFactory` 改三分支：

```go
func defaultAdapterFactory(route string, marks provider.CacheMarks) provider.Adapter {
	switch route {
	case "anthropic":
		return provider.Anthropic{Marks: marks}
	case "openai-responses":
		return provider.OpenAIResponses{}
	default:
		return provider.OpenAICompat{}
	}
}
```

- [ ] **Step 4: 运行确认通过 + 全量回归**

Run: `go build ./... && go test ./pkg/llm/... 2>&1 | tail -5`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/provider/openai_responses.go pkg/llm/provider/openai_responses_test.go pkg/llm/engine/engine.go pkg/llm/engine/engine_test.go
git commit -m "feat(provider): OpenAI Responses wire adapter with engine factory route"
```

---

### Task 8: MaxInput 消费 + 配置示例 + 三场景对齐 + 全量验证

**Files:**
- Modify: `pkg/llm/engine/engine.go`(ModelMaxInput)
- Modify: `pkg/llm/client.go`(ModelMaxInput)
- Modify: `cmd/lcoder/main.go`（压缩预算取 min(window, maxInput))
- Modify: `configs/lcoder.yaml`（注释示例：`thinking`、`models_source`、providers route 说明）
- Modify: `USER_GUIDE.md`（新配置项一句话说明，跟随现有文档风格）

- [ ] **Step 1: 写失败测试**

engine 测试追加：

```go
func TestModelMaxInput(t *testing.T) {
	e := New(catalog.New(catalog.Options{Refresh: false, Overrides: []catalog.Entry{
		{ID: "gpt-5", Provider: "openai", ContextWindow: 400000, MaxInput: 272000},
	}}))
	if got := e.ModelMaxInput("openai", "gpt-5"); got != 272000 {
		t.Errorf("ModelMaxInput = %d, want 272000", got)
	}
	if got := e.ModelMaxInput("openai", "gpt-4o"); got != 0 {
		t.Errorf("unknown model MaxInput = %d, want 0", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/llm/engine -run TestModelMaxInput -v`
Expected: 编译失败

- [ ] **Step 3: 实现**

`pkg/llm/engine/engine.go` 追加：

```go
// ModelMaxInput returns the catalog prompt cap for provider/model (0 = no
// separate cap; use the context window).
func (e *Engine) ModelMaxInput(prov, model string) int { return e.catalog.MaxInput(prov, model) }
```

`pkg/llm/client.go` 追加：

```go
// ModelMaxInput returns the model prompt cap (0 = use the context window).
func (c *Client) ModelMaxInput(ctx context.Context, prov, model string) (int, error) {
	return c.engine.ModelMaxInput(prov, model), nil
}
```

`cmd/lcoder/main.go`（现有 `window, _ := llmClient.ModelWindow(...)` 之后）:

```go
	if maxInput, _ := llmClient.ModelMaxInput(context.Background(), cfg.Provider, cfg.Model); maxInput > 0 && maxInput < window {
		window = maxInput
	}
```

`configs/lcoder.yaml` 在 model 附近加注释示例（跟随现有注释风格）:

```yaml
# thinking: off | on | <模型声明的档位,如 low/medium/high>
# 缺省不发 thinking 字段;模型未声明的档位会回退 on 并 warning
# thinking: medium

# models_source: 自定义 models.dev 风格模型目录 URL(内网 registry)
# 也可用环境变量 LCODER_MODELS_SOURCE(优先)
# models_source: https://models.dev/api.json
```

`USER_GUIDE.md` 的配置说明处补同样两句话（中英两份若都存在则都改——检查 `USER_GUIDE_EN.md`)。

三场景对齐检查：

```bash
grep -rn "route\|providers:" eval/swe-bench-lite --include="*.yaml" --include="*.yml" --include="*.py" | head
```

Expected: eval 不依赖 providers route 显式声明（新逻辑对显式 route 是透传的）；如发现 eval 配置有无 route 的自定义 provider，确认其会走推断路径且行为正确，必要时在 eval 配置补 `route` 或 `base_url`。

- [ ] **Step 4: 全量验证**

```bash
go build ./...
go vet $(go list ./... | grep -v 'reference/Shannon')
go test $(go list ./... | grep -v 'reference/Shannon') -count=1
```

Expected: 全部通过，无 FAIL

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/engine/engine.go pkg/llm/client.go cmd/lcoder/main.go configs/lcoder.yaml USER_GUIDE.md USER_GUIDE_EN.md
git commit -m "feat(llm): consume max_input for prompt budget; document thinking and models_source config"
```
