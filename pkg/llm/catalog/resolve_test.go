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

func TestResolveExplicitProtocolPassThrough(t *testing.T) {
	c := newResolveCatalog()
	res, err := c.ResolveProvider("deepseek", "openai-chat", "https://api.deepseek.com/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Protocol != provider.ProtocolOpenAIChat || res.Guessed {
		t.Errorf("explicit protocol must pass through unguessed: %+v", res)
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
	if res.Protocol != provider.ProtocolAnthropic || !res.Guessed {
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
	if res.Protocol != provider.ProtocolOpenAIResponses {
		t.Errorf("protocol = %q, want openai-responses", res.Protocol)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	c := newResolveCatalog()
	// 有 base_url → openai-chat + guessed
	res, err := c.ResolveProvider("my-relay", "", "http://localhost:4000/v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Protocol != provider.ProtocolOpenAIChat || !res.Guessed || res.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("got %+v", res)
	}
	// 无 base_url → 报错(未知 provider 没有已知端点)
	if _, err := c.ResolveProvider("my-relay", "", ""); err == nil {
		t.Fatal("unknown provider without base_url must error")
	}
}

func TestResolveRejectsBadBaseURL(t *testing.T) {
	c := newResolveCatalog()
	if _, err := c.ResolveProvider("x", "openai-chat", "   "); err == nil {
		t.Error("blank base_url must error")
	}
	if _, err := c.ResolveProvider("x", "openai-chat", "https://${HOST}/v1"); err == nil {
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

func TestResolveUnknownProviderNoBaseURLError(t *testing.T) {
	c := newResolveCatalog()
	_, err := c.ResolveProvider("my-relay", "", "")
	if err == nil {
		t.Fatal("unknown provider with no base URL must error")
	}
	if !strings.Contains(err.Error(), "no base URL known") {
		t.Errorf("err = %q, want mention of missing base URL", err)
	}
}

func TestResolveKnownNameDefaultBase(t *testing.T) {
	c := newResolveCatalog()
	// 显式 protocol + 无 base + 内置已知名:命中 name 默认表。
	// 用 kimi-code 而非 deepseek:重生后的 snapshot 带 deepseek provider meta,
	// catalog api 分支会先于 name 默认表命中;kimi-code 不在 models.dev 收录内。
	res, err := c.ResolveProvider("kimi-code", "openai-chat", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.BaseURL != provider.DefaultBaseURL("kimi-code") {
		t.Errorf("base = %q, want name default %q", res.BaseURL, provider.DefaultBaseURL("kimi-code"))
	}
	if res.Guessed {
		t.Error("explicit protocol must not be flagged as guessed")
	}
}

func TestResolveSkipsPlaceholderCatalogAPI(t *testing.T) {
	c := newResolveCatalog()
	c.mergeDataset(Dataset{Providers: []ProviderMeta{
		{ID: "p1", API: "https://${HOST}/v1"},
	}})
	// catalog api 含占位符应被跳过;未知 provider 名无默认 base → 报错,
	// 而不是回落到某个协议的默认端点(键可能被打到错误主机)。
	if _, err := c.ResolveProvider("p1", "", ""); err == nil {
		t.Fatal("placeholder catalog api with unknown provider must error")
	}
}

func TestResolveProviderExplicitProtocol(t *testing.T) {
	c := New(Options{Refresh: false})
	// 显式 protocol 覆盖 route 派生:自建代理说 anthropic 协议。
	res, err := c.ResolveProvider("my-proxy", "anthropic", "http://localhost:4000/v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Protocol != provider.ProtocolAnthropic {
		t.Fatalf("protocol = %q, want anthropic", res.Protocol)
	}
	// 非法 protocol 是配置错误,不得沉默兜底。
	if _, err := c.ResolveProvider("my-proxy", "gpt", "http://localhost:4000/v1"); err == nil {
		t.Fatal("unknown protocol must be rejected")
	}
	// 缺省按 provider 名推断。
	res, err = c.ResolveProvider("deepseek", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Protocol != provider.ProtocolOpenAIChat {
		t.Fatalf("protocol = %q, want openai-chat", res.Protocol)
	}
}
