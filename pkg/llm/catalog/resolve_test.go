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
	// catalog api 带 /v1,按原样使用:anthropic 适配器只拼 /messages
	if res.BaseURL != "https://api.anthropic.com/v1" {
		t.Errorf("base = %q, want /v1 kept", res.BaseURL)
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
	// anthropic + 显式带 /v1 的 URL 按原样透传
	res, err := c.ResolveProvider("x", "anthropic", "https://proxy.example.com/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.BaseURL != "https://proxy.example.com/v1" {
		t.Errorf("anthropic base must pass through verbatim, got %q", res.BaseURL)
	}
}

func TestResolveUnknownRouteNoBaseURLError(t *testing.T) {
	c := newResolveCatalog()
	_, err := c.ResolveProvider("my-relay", "weird-route", "")
	if err == nil {
		t.Fatal("unknown route with no base URL must error")
	}
	if !strings.Contains(err.Error(), "no base URL known") {
		t.Errorf("err = %q, want mention of missing base URL", err)
	}
}

func TestResolveExplicitRouteNameDefaultBase(t *testing.T) {
	c := newResolveCatalog()
	// 显式 route + 无 base + 内置已知名:命中 name 默认表
	res, err := c.ResolveProvider("deepseek", "openai", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.BaseURL != provider.DefaultBaseURL("deepseek") {
		t.Errorf("base = %q, want name default %q", res.BaseURL, provider.DefaultBaseURL("deepseek"))
	}
	if res.Guessed {
		t.Error("explicit route must not be flagged as guessed")
	}
}

func TestResolveSkipsPlaceholderCatalogAPI(t *testing.T) {
	c := newResolveCatalog()
	c.mergeDataset(Dataset{Providers: []ProviderMeta{
		{ID: "p1", API: "https://${HOST}/v1"},
	}})
	// catalog api 含占位符应被跳过,回落到 route(openai)默认 base
	res, err := c.ResolveProvider("p1", "", "")
	if err != nil {
		t.Fatalf("placeholder catalog api must fall back, not error: %v", err)
	}
	if strings.Contains(res.BaseURL, "${") {
		t.Errorf("base must not be the placeholder URL, got %q", res.BaseURL)
	}
	if res.BaseURL != provider.DefaultBaseURL("openai") {
		t.Errorf("base = %q, want openai default %q", res.BaseURL, provider.DefaultBaseURL("openai"))
	}
}
