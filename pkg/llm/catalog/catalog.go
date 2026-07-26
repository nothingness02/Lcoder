// pkg/llm/catalog/catalog.go
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/pkg/llm/pricing"
	"github.com/lcoder/lcoder/pkg/models"
)

//go:embed snapshot.json
var snapshotJSON []byte

const (
	modelsDevURL = "https://models.dev/api.json"
	cacheTTL     = 5 * time.Minute
)

// providerAliases maps Lcoder's canonical provider names to the provider keys
// models.dev files records under. Lcoder surfaces "moonshot" in its picker, but
// models.dev keys the same models as "moonshotai", so a lookup by the canonical
// name has to fall back to the upstream key to resolve window/output/pricing.
var providerAliases = map[string]string{
	"moonshot": "moonshotai",
	"gemini":   "google",
	"Zai":      "zhipuai",
}

// providerCandidates returns the provider names to try for a lookup: the given
// name first, then its models.dev alias (if any). Ordering keeps an exact,
// same-name match ahead of the aliased one.
func providerCandidates(provider string) []string {
	if alias, ok := providerAliases[provider]; ok {
		return []string{provider, alias}
	}
	return []string{provider}
}

// Entry is one catalog model record (snapshot/models.dev shape).
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

// Options configures catalog construction.
type Options struct {
	Refresh   bool    // enable models.dev background refresh
	CachePath string  // ~/.lcoder/cache/models.json
	SourceURL string  // models.dev endpoint (default https://models.dev/api.json)
	Overrides []Entry // from models.yaml (highest priority)
}

// Catalog holds merged model entries keyed by "provider/id".
type Catalog struct {
	mu        sync.RWMutex
	entries   map[string]Entry
	providers map[string]ProviderMeta
	order     []string
	overrides []Entry
	sourceURL string
}

// New builds a catalog from the embedded snapshot, applies overrides, and (if
// Options.Refresh) kicks off a non-blocking background refresh.
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
		// Legacy []Entry snapshot format (pre-regeneration transitional path).
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

// mergeDataset merges provider metadata, then model entries.
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

func (c *Catalog) merge(entries []Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range entries {
		key := e.Provider + "/" + e.ID
		existing, exists := c.entries[key]
		if !exists {
			c.order = append(c.order, key)
		} else {
			// Preserve non-zero fields from the incoming entry; leave existing
			// values in place when the override is silent (zero / empty).
			if e.Name != "" {
				existing.Name = e.Name
			}
			if e.ContextWindow > 0 {
				existing.ContextWindow = e.ContextWindow
			}
			if e.MaxOutput > 0 {
				existing.MaxOutput = e.MaxOutput
			}
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
			if len(e.Capabilities) > 0 {
				existing.Capabilities = e.Capabilities
			}
			if e.Cost.Prompt > 0 || e.Cost.Completion > 0 {
				existing.Cost = e.Cost
			}
			e = existing
		}
		c.entries[key] = e
	}
}

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

// List returns all models as ModelInfo in stable insertion order.
func (c *Catalog) List() []models.ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]models.ModelInfo, 0, len(c.order))
	for _, key := range c.order {
		e := c.entries[key]
		out = append(out, models.ModelInfo{
			ID:            e.ID,
			Name:          e.Name,
			Provider:      e.Provider,
			Capabilities:  e.Capabilities,
			ContextWindow: e.ContextWindow,
		})
	}
	return out
}

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

// Window returns the context window for provider/model: exact match first, then
// a prefix match (either direction) so dated variants resolve, with the static
// fallback table as last resort. 0 if unknown.
func (c *Catalog) Window(provider, model string) int {
	if e, ok := c.lookup(provider, model); ok && e.ContextWindow > 0 {
		return e.ContextWindow
	}
	if fb, ok := LookupFallback(provider, model); ok {
		return fb.ContextWindow // 可能为 0,由下游走默认值
	}
	return 0
}

// MaxOutput returns the single-response output ceiling for provider/model using
// the same exact-then-prefix matching as Window, static table as last resort.
// 0 if unknown.
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

// PriceTable returns a pricing table for pricing.EstimateCost, catalog entries
// overlaid on the built-in defaults.
func (c *Catalog) PriceTable() map[string]pricing.ModelPrice {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := pricing.DefaultPricing()
	for key, e := range c.entries {
		if e.Cost.Prompt == 0 && e.Cost.Completion == 0 {
			continue
		}
		out[key] = pricing.ModelPrice{
			Prompt: e.Cost.Prompt, Completion: e.Cost.Completion,
			CacheRead: e.Cost.CacheRead, CacheWrite: e.Cost.CacheWrite,
		}
	}
	// Mirror models.dev prices under Lcoder's canonical provider name so cost
	// lookups keyed by the picker name (e.g. "moonshot/...") resolve records the
	// snapshot files under its upstream alias ("moonshotai/..."). Existing
	// canonical-name entries win and are never overwritten.
	for canonical, dev := range providerAliases {
		for key, e := range c.entries {
			id, ok := strings.CutPrefix(key, dev+"/")
			if !ok || (e.Cost.Prompt == 0 && e.Cost.Completion == 0) {
				continue
			}
			mirrored := canonical + "/" + id
			if _, exists := out[mirrored]; exists {
				continue
			}
			out[mirrored] = pricing.ModelPrice{
				Prompt: e.Cost.Prompt, Completion: e.Cost.Completion,
				CacheRead: e.Cost.CacheRead, CacheWrite: e.Cost.CacheWrite,
			}
		}
	}
	return out
}

// refresh loads models.dev data (from a fresh cache if within TTL, else over the
// network), merges it under the user overrides, and rewrites the cache. Any
// failure is swallowed: the embedded snapshot remains in effect.
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

// applyRefresh merges models.dev entries, then re-asserts user overrides on top.
func (c *Catalog) applyRefresh(ds Dataset) {
	c.mergeDataset(ds)
	c.merge(c.overrides)
}

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
		ds.Providers = append(ds.Providers, ProviderMeta{ID: provID, Npm: p.Npm, API: p.API, Env: p.Env})
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
					offEffort = strings.ToLower(s)
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
// models that do not emit text.
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
