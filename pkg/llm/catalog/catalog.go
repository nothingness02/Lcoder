// pkg/llm/catalog/catalog.go
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	ContextWindow int      `json:"context_window"`
	MaxOutput     int      `json:"max_output"`
	Capabilities  []string `json:"capabilities"`
	Cost          struct {
		Prompt     float64 `json:"prompt"`
		Completion float64 `json:"completion"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
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
	c := &Catalog{entries: map[string]Entry{}, overrides: opts.Overrides, sourceURL: src}
	var snap []Entry
	_ = json.Unmarshal(snapshotJSON, &snap)
	c.merge(snap)
	c.merge(opts.Overrides)
	if opts.Refresh {
		go c.refresh(opts.CachePath)
	}
	return c
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

// Window returns the context window for provider/model: exact match first, then
// a prefix match (either direction) so dated variants resolve. 0 if unknown.
func (c *Catalog) Window(provider, model string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range providerCandidates(provider) {
		if e, ok := c.entries[p+"/"+model]; ok && e.ContextWindow > 0 {
			return e.ContextWindow
		}
		for _, key := range c.order {
			e := c.entries[key]
			if e.Provider != p || e.ContextWindow <= 0 {
				continue
			}
			if strings.HasPrefix(e.ID, model) || strings.HasPrefix(model, e.ID) {
				return e.ContextWindow
			}
		}
	}
	return 0
}

// MaxOutput returns the single-response output ceiling for provider/model using
// the same exact-then-prefix matching as Window. 0 if unknown.
func (c *Catalog) MaxOutput(provider, model string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range providerCandidates(provider) {
		if e, ok := c.entries[p+"/"+model]; ok && e.MaxOutput > 0 {
			return e.MaxOutput
		}
		for _, key := range c.order {
			e := c.entries[key]
			if e.Provider != p || e.MaxOutput <= 0 {
				continue
			}
			if strings.HasPrefix(e.ID, model) || strings.HasPrefix(model, e.ID) {
				return e.MaxOutput
			}
		}
	}
	return 0
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
				var ents []Entry
				if json.Unmarshal(data, &ents) == nil && len(ents) > 0 {
					c.applyRefresh(ents)
					return
				}
			}
		}
	}
	ents, err := fetchModelsDev(c.sourceURL)
	if err != nil || len(ents) == 0 {
		return
	}
	if cachePath != "" {
		if data, err := json.Marshal(ents); err == nil {
			_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
			_ = os.WriteFile(cachePath, data, 0o644)
		}
	}
	c.applyRefresh(ents)
}

// applyRefresh merges models.dev entries, then re-asserts user overrides on top.
func (c *Catalog) applyRefresh(ents []Entry) {
	c.merge(ents)
	c.merge(c.overrides)
}

// fetchModelsDev fetches and flattens the models.dev api.json into []Entry.
func fetchModelsDev(url string) ([]Entry, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned %d", resp.StatusCode)
	}
	var raw map[string]struct {
		Models map[string]struct {
			Name  string `json:"name"`
			Limit struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
			Cost struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cache_read"`
				CacheWrite float64 `json:"cache_write"`
			} `json:"cost"`
			ToolCall  bool `json:"tool_call"`
			Reasoning bool `json:"reasoning"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []Entry
	for provID, p := range raw {
		for modelID, m := range p.Models {
			e := Entry{ID: modelID, Name: m.Name, Provider: provID, ContextWindow: m.Limit.Context, MaxOutput: m.Limit.Output}
			if m.ToolCall {
				e.Capabilities = append(e.Capabilities, "tools")
			}
			if m.Reasoning {
				e.Capabilities = append(e.Capabilities, "reasoning")
			}
			e.Cost.Prompt = m.Cost.Input
			e.Cost.Completion = m.Cost.Output
			e.Cost.CacheRead = m.Cost.CacheRead
			e.Cost.CacheWrite = m.Cost.CacheWrite
			out = append(out, e)
		}
	}
	return out, nil
}
