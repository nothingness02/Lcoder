// pkg/llm/catalog/resolve_test.go
package catalog

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm/provider"
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
	// 有 base_url → openai + guessed
	res, err := c.ResolveProvider("my-relay", "", "http://localhost:4000/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Route != "openai" || !res.Guessed || res.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("got %+v", res)
	}
	// 无 base_url → 落到 openai 默认端点 + guessed
	res2, err := c.ResolveProvider("my-relay", "", "")
	if err != nil {
		t.Fatalf("unknown provider should degrade to openai default: %v", err)
	}
	if res2.Route != "openai" || !res2.Guessed || res2.BaseURL != provider.DefaultBaseURL("openai") {
		t.Errorf("got %+v", res2)
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
	// anthropic + 显式带 /v1 的 URL 也要剥
	res, err := c.ResolveProvider("x", "anthropic", "https://proxy.example.com/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.HasSuffix(res.BaseURL, "/v1") {
		t.Errorf("anthropic base must strip /v1, got %q", res.BaseURL)
	}
}
