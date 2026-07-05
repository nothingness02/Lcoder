# 网关 model 路由 + 文件凭据层 实现计划(计划一/共二)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让网关按传入的 model 自行解析 provider 并路由,新增独立 `credentials.yaml` 凭据层与运行中热加 provider 的 reload 端点,使纯文件配置即可使用多供应商。

**Architecture:** 把 "model→provider" 路由权威下沉到网关(`registry.resolve_provider`),`TurnRequest.Model.Provider` 降为可选;Go 侧新增 `credentials.yaml` 加载并合并进现有 `cfg.Providers`(经 `LCODER_PROVIDERS` 流向网关);网关新增 `POST /v1/providers` 热更新连接表,Go 侧提供 `client.RegisterProvider` 库调用。

**Tech Stack:** Go(koanf/yaml.v3/net-http/httptest)、Python(FastAPI/LiteLLM/pydantic/pytest)。

**对应 spec:** `docs/superpowers/specs/2026-06-28-gateway-model-routing-tui-config-design.md` 的 §4/§5/§6/§7/§10/§11(§8 TUI、§9 首启向导属计划二)。

**测试命令前置:** 网关测试在 `gateway/` 下用其虚拟环境运行:`cd gateway && .venv/Scripts/python -m pytest`(Windows 路径;bash 中 forward slash)。Go 测试:`go test ./pkg/... ./cmd/...`。

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `gateway/lcoder_gateway/registry.py` | 新增 `resolve_provider(model_id)` | 修改 |
| `gateway/lcoder_gateway/models.py` | `ModelRef.provider` 可选;新增 `ProviderConnIn` | 修改 |
| `gateway/lcoder_gateway/server.py` | `resolve_turn_provider` 接线 `stream_turn`;`POST /v1/providers` | 修改 |
| `gateway/tests/test_registry.py` | `resolve_provider` 测试 | 修改 |
| `gateway/tests/test_server.py` | provider 解析 + reload 测试 | 创建 |
| `pkg/models/message.go` | `ModelRef.Provider` 改 `omitempty` | 修改 |
| `pkg/config/builtin_providers.go` | 内置 provider 表 | 创建 |
| `pkg/config/builtin_providers_test.go` | 表查找测试 | 创建 |
| `pkg/config/credentials.go` | `credentials.yaml` 加载/合并/保存 | 创建 |
| `pkg/config/credentials_test.go` | 凭据测试 | 创建 |
| `pkg/config/config.go` | `Load()` 接入 credentials 合并 | 修改 |
| `pkg/llm/client.go` | `RegisterProvider` | 修改 |
| `pkg/llm/register_test.go` | `RegisterProvider` 测试 | 创建 |
| `.gitignore` / `configs/lcoder.yaml` | 忽略 credentials;文档同步 | 修改 |

---

## Task 1: 网关 `registry.resolve_provider`

**Files:**
- Modify: `gateway/lcoder_gateway/registry.py`
- Test: `gateway/tests/test_registry.py`

- [ ] **Step 1: 写失败测试**

在 `gateway/tests/test_registry.py` 末尾追加:

```python
class TestResolveProvider:
    def test_resolves_known_catalog_model(self) -> None:
        registry = ModelRegistry(enable_discovery=False)
        assert registry.resolve_provider("gpt-4o") == "openai"
        assert registry.resolve_provider("deepseek-chat") == "deepseek"

    def test_falls_back_to_litellm_inference(self, monkeypatch: pytest.MonkeyPatch) -> None:
        registry = ModelRegistry(enable_discovery=False)
        monkeypatch.setattr(
            "lcoder_gateway.registry.litellm.get_llm_provider",
            lambda model_id: (model_id, "openai", None, None),
        )
        assert registry.resolve_provider("o1-preview") == "openai"

    def test_returns_none_for_unknown_model(self, monkeypatch: pytest.MonkeyPatch) -> None:
        registry = ModelRegistry(enable_discovery=False)

        def boom(model_id: str):
            raise Exception("unknown model")

        monkeypatch.setattr("lcoder_gateway.registry.litellm.get_llm_provider", boom)
        assert registry.resolve_provider("totally-unknown-xyz") is None
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd gateway && .venv/Scripts/python -m pytest tests/test_registry.py::TestResolveProvider -v`
Expected: FAIL —`AttributeError: 'ModelRegistry' object has no attribute 'resolve_provider'`

- [ ] **Step 3: 实现**

在 `registry.py` 的 `ModelRegistry` 类内,`get_model` 方法之后新增:

```python
    def resolve_provider(self, model_id: str) -> str | None:
        """Resolve the provider for a model id when the caller omits it.

        Tries the known registry first (any provider whose model id matches),
        then falls back to litellm's built-in provider inference. Returns None
        when the model cannot be resolved.
        """
        for model in self._models.values():
            if model.id == model_id:
                return model.provider
        try:
            _, provider, _, _ = litellm.get_llm_provider(model_id)
        except Exception:
            return None
        return LITELLM_PROVIDER_MAP.get(provider, provider) or None
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd gateway && .venv/Scripts/python -m pytest tests/test_registry.py::TestResolveProvider -v`
Expected: PASS(3 passed)

- [ ] **Step 5: 提交**

```bash
git add gateway/lcoder_gateway/registry.py gateway/tests/test_registry.py
git commit -m "feat(gateway): resolve provider from model id in registry"
```

---

## Task 2: `ModelRef.provider` 可选 + `stream_turn` 缺省解析

**Files:**
- Modify: `gateway/lcoder_gateway/models.py:8-10`
- Modify: `gateway/lcoder_gateway/server.py`(`stream_turn` 内 litellm 调用)
- Modify: `pkg/models/message.go:307-310`
- Test: `gateway/tests/test_server.py`(创建)

- [ ] **Step 1: 写失败测试**

创建 `gateway/tests/test_server.py`:

```python
from __future__ import annotations

import pytest

from lcoder_gateway import server
from lcoder_gateway.models import ModelRef


class TestResolveTurnProvider:
    def test_explicit_provider_wins(self) -> None:
        ref = ModelRef(provider="anthropic", id="claude-sonnet-4-20250514")
        assert server.resolve_turn_provider(ref) == "anthropic"

    def test_infers_when_omitted(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setattr(server.model_registry, "resolve_provider", lambda mid: "openai")
        assert server.resolve_turn_provider(ModelRef(id="gpt-4o")) == "openai"

    def test_empty_when_unknown(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setattr(server.model_registry, "resolve_provider", lambda mid: None)
        assert server.resolve_turn_provider(ModelRef(id="mystery")) == ""
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd gateway && .venv/Scripts/python -m pytest tests/test_server.py -v`
Expected: FAIL —`pydantic ValidationError`(provider 必填)或 `AttributeError: module 'lcoder_gateway.server' has no attribute 'resolve_turn_provider'`

- [ ] **Step 3a: 让 `ModelRef.provider` 可选**

`gateway/lcoder_gateway/models.py` 第 8-10 行改为:

```python
class ModelRef(BaseModel):
    provider: str = ""
    id: str
```

- [ ] **Step 3b: 新增解析函数并接线 `stream_turn`**

在 `server.py` 的 `_provider_conns = load_provider_conns()`(第 38 行)之后新增:

```python
def resolve_turn_provider(model: ModelRef) -> str:
    """Return the explicit provider, or infer it from the registry when omitted."""
    if model.provider:
        return model.provider
    return model_registry.resolve_provider(model.id) or ""
```

在 `stream_turn`(第 126 行起)内,把第 140-149 行的 `litellm.acompletion(...)` 调用改为先解析 provider:

```python
    try:
        provider = resolve_turn_provider(request.model)
        response = await litellm.acompletion(
            model=litellm_model(provider, request.model.id, _provider_conns),
            messages=[{"role": "system", "content": request.system_prompt}, *messages],
            tools=tools,
            temperature=generation.temperature,
            max_tokens=generation.max_tokens,
            top_p=generation.top_p,
            stream=True,
            **completion_overrides(provider, _provider_conns),
        )
```

(仅把原先两处 `request.model.provider` 替换为局部变量 `provider`;其余行不变。)

- [ ] **Step 3c: Go 侧 `ModelRef.Provider` 改可选**

`pkg/models/message.go` 第 307-310 行改为:

```go
// ModelRef identifies a specific model through the Gateway. Provider is
// optional: when empty, the gateway resolves it from the model id.
type ModelRef struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id"`
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd gateway && .venv/Scripts/python -m pytest tests/test_server.py -v`
Expected: PASS(3 passed)
Run: `go build ./...`
Expected: 无错误

- [ ] **Step 5: 提交**

```bash
git add gateway/lcoder_gateway/models.py gateway/lcoder_gateway/server.py gateway/tests/test_server.py pkg/models/message.go
git commit -m "feat(gateway): make model provider optional, resolve in stream_turn"
```

---

## Task 3: 网关 `POST /v1/providers` 热更新端点

**Files:**
- Modify: `gateway/lcoder_gateway/models.py`(新增 `ProviderConnIn`)
- Modify: `gateway/lcoder_gateway/server.py`(`register_provider_conn` + 端点)
- Test: `gateway/tests/test_server.py`

- [ ] **Step 1: 写失败测试**

在 `gateway/tests/test_server.py` 末尾追加:

```python
from lcoder_gateway.providers import completion_overrides


class TestRegisterProviderConn:
    def test_updates_conn_table_and_overrides(self) -> None:
        server.register_provider_conn(
            "moonshot",
            {"base_url": "https://api.moonshot.cn/v1", "api_key": "sk", "route": None, "headers": None},
        )
        assert "moonshot" in server._provider_conns
        out = completion_overrides("moonshot", server._provider_conns)
        assert out == {"api_base": "https://api.moonshot.cn/v1", "api_key": "sk"}
        del server._provider_conns["moonshot"]  # avoid cross-test leakage

    def test_drops_none_fields(self) -> None:
        server.register_provider_conn("tmp", {"api_key": "k", "base_url": None, "route": None, "headers": None})
        assert server._provider_conns["tmp"] == {"api_key": "k"}
        del server._provider_conns["tmp"]
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd gateway && .venv/Scripts/python -m pytest tests/test_server.py::TestRegisterProviderConn -v`
Expected: FAIL —`AttributeError: module 'lcoder_gateway.server' has no attribute 'register_provider_conn'`

- [ ] **Step 3a: 新增 `ProviderConnIn` 模型**

在 `gateway/lcoder_gateway/models.py` 末尾追加:

```python
class ProviderConnIn(BaseModel):
    name: str
    base_url: str | None = None
    api_key: str | None = None
    route: str | None = None
    headers: dict[str, str] | None = None
```

- [ ] **Step 3b: 新增更新函数与端点**

在 `server.py` 的 import 块把 `models` 导入补上 `ProviderConnIn`(第 15-23 行的 from .models import 列表里加一行 `ProviderConnIn,`)。

在 `resolve_turn_provider` 函数之后新增:

```python
def register_provider_conn(name: str, conn: dict[str, Any]) -> None:
    """Hot-update the in-memory provider connection table (drops None fields)."""
    _provider_conns[name] = {k: v for k, v in conn.items() if v is not None}
```

在文件末尾的端点区(`@app.get("/v1/models")` 等附近)新增:

```python
@app.post("/v1/providers")
async def register_provider(body: ProviderConnIn) -> dict[str, str]:
    register_provider_conn(
        body.name,
        {"base_url": body.base_url, "api_key": body.api_key, "route": body.route, "headers": body.headers},
    )
    return {"status": "ok"}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd gateway && .venv/Scripts/python -m pytest tests/test_server.py -v`
Expected: PASS(全部 server 测试通过)

- [ ] **Step 5: 提交**

```bash
git add gateway/lcoder_gateway/models.py gateway/lcoder_gateway/server.py gateway/tests/test_server.py
git commit -m "feat(gateway): add POST /v1/providers to hot-register a provider conn"
```

---

## Task 4: Go 内置 provider 表

**Files:**
- Create: `pkg/config/builtin_providers.go`
- Test: `pkg/config/builtin_providers_test.go`

- [ ] **Step 1: 写失败测试**

创建 `pkg/config/builtin_providers_test.go`:

```go
package config

import "testing"

func TestBuiltinProviderLookup(t *testing.T) {
	p, ok := BuiltinProvider("openai")
	if !ok || p.KeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("expected openai key env, got %+v ok=%v", p, ok)
	}
	if _, ok := BuiltinProvider("nope"); ok {
		t.Fatal("expected miss for unknown provider")
	}
}

func TestBuiltinProviderMoonshot(t *testing.T) {
	p, ok := BuiltinProvider("moonshot")
	if !ok || p.DefaultBase != "https://api.moonshot.cn/v1" || p.Route != "openai" {
		t.Fatalf("unexpected moonshot entry: %+v ok=%v", p, ok)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/config/ -run TestBuiltinProvider -v`
Expected: FAIL —`undefined: BuiltinProvider`

- [ ] **Step 3: 实现**

创建 `pkg/config/builtin_providers.go`:

```go
package config

// ProviderInfo describes a built-in provider entry surfaced in the TUI picker
// and used to map a provider to the standard env var holding its api key.
type ProviderInfo struct {
	Name        string // internal id, e.g. "openai"
	Display     string // human-facing name for the TUI
	KeyEnv      string // standard api key environment variable
	Route       string // litellm protocol prefix (defaults to Name)
	DefaultBase string // non-standard base_url; empty when litellm's default applies
}

// BuiltinProviders is the curated list of common providers shown in the TUI.
// litellm already knows most providers' base_url/route; this table only adds
// what the UI needs (display name, key env) plus a few non-standard bases.
var BuiltinProviders = []ProviderInfo{
	{Name: "openai", Display: "OpenAI", KeyEnv: "OPENAI_API_KEY", Route: "openai"},
	{Name: "anthropic", Display: "Anthropic", KeyEnv: "ANTHROPIC_API_KEY", Route: "anthropic"},
	{Name: "deepseek", Display: "DeepSeek", KeyEnv: "DEEPSEEK_API_KEY", Route: "deepseek"},
	{Name: "moonshot", Display: "Moonshot (Kimi)", KeyEnv: "MOONSHOT_API_KEY", Route: "openai", DefaultBase: "https://api.moonshot.cn/v1"},
	{Name: "gemini", Display: "Google Gemini", KeyEnv: "GEMINI_API_KEY", Route: "gemini"},
	{Name: "openrouter", Display: "OpenRouter", KeyEnv: "OPENROUTER_API_KEY", Route: "openrouter"},
}

// BuiltinProvider returns the built-in entry for the given provider name.
func BuiltinProvider(name string) (ProviderInfo, bool) {
	for _, p := range BuiltinProviders {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderInfo{}, false
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/config/ -run TestBuiltinProvider -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/config/builtin_providers.go pkg/config/builtin_providers_test.go
git commit -m "feat(config): add built-in provider table for TUI and key env mapping"
```

---

## Task 5: `credentials.yaml` 加载 / 合并 / 保存

**Files:**
- Create: `pkg/config/credentials.go`
- Test: `pkg/config/credentials_test.go`

- [ ] **Step 1: 写失败测试**

创建 `pkg/config/credentials_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentialsMissingReturnsEmpty(t *testing.T) {
	creds, err := LoadCredentials(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("expected empty, got %+v", creds)
	}
}

func TestLoadCredentialsParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.yaml")
	body := "openai:\n  api_key: sk-open\nmoonshot:\n  api_key: sk-moon\n  base_url: https://api.moonshot.cn/v1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds["openai"].APIKey != "sk-open" {
		t.Fatalf("openai key wrong: %+v", creds["openai"])
	}
	if creds["moonshot"].BaseURL != "https://api.moonshot.cn/v1" {
		t.Fatalf("moonshot base wrong: %+v", creds["moonshot"])
	}
}

func TestMergeCredentialsFillsGapsWithoutOverriding(t *testing.T) {
	providers := map[string]ProviderConn{
		"openai": {APIKey: "from-config"}, // hand-written wins
	}
	creds := map[string]ProviderConn{
		"openai":   {APIKey: "from-creds", BaseURL: "https://x"},
		"moonshot": {APIKey: "sk-moon"},
	}
	out := mergeCredentials(providers, creds)
	if out["openai"].APIKey != "from-config" {
		t.Fatalf("config api_key must win, got %q", out["openai"].APIKey)
	}
	if out["openai"].BaseURL != "https://x" {
		t.Fatalf("missing base_url should be filled from creds, got %q", out["openai"].BaseURL)
	}
	if out["moonshot"].APIKey != "sk-moon" {
		t.Fatalf("new provider should be added, got %+v", out["moonshot"])
	}
}

func TestSaveCredentialsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "credentials.yaml")
	in := map[string]ProviderConn{"openai": {APIKey: "sk"}}
	if err := SaveCredentials(path, in); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	out, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if out["openai"].APIKey != "sk" {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/config/ -run 'Credentials' -v`
Expected: FAIL —`undefined: LoadCredentials`

- [ ] **Step 3: 实现**

创建 `pkg/config/credentials.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LoadCredentials reads a credentials.yaml mapping provider name to connection
// settings (api_key plus optional base_url/route/headers). A missing file
// returns an empty map (not an error).
func LoadCredentials(path string) (map[string]ProviderConn, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ProviderConn{}, nil
		}
		return nil, fmt.Errorf("read credentials %s: %w", path, err)
	}
	var creds map[string]ProviderConn
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	if creds == nil {
		creds = map[string]ProviderConn{}
	}
	return creds, nil
}

// resolveCredentialsPath returns ~/.lcoder/credentials.yaml (empty if no home).
func resolveCredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lcoder", "credentials.yaml")
}

// mergeCredentials folds creds into providers without overriding fields already
// set in providers — hand-written config.providers wins over TUI credentials.
func mergeCredentials(providers, creds map[string]ProviderConn) map[string]ProviderConn {
	if len(creds) == 0 {
		return providers
	}
	if providers == nil {
		providers = map[string]ProviderConn{}
	}
	for name, cred := range creds {
		existing, ok := providers[name]
		if !ok {
			providers[name] = cred
			continue
		}
		if existing.APIKey == "" {
			existing.APIKey = cred.APIKey
		}
		if existing.BaseURL == "" {
			existing.BaseURL = cred.BaseURL
		}
		if existing.Route == "" {
			existing.Route = cred.Route
		}
		if existing.Headers == nil {
			existing.Headers = cred.Headers
		}
		providers[name] = existing
	}
	return providers
}

// SaveCredentials writes creds to path with 0600 permissions, creating the
// parent directory as needed. Used by the TUI to persist entered api keys.
func SaveCredentials(path string, creds map[string]ProviderConn) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	data, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credentials %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/config/ -run 'Credentials' -v`
Expected: PASS(4 passed)

- [ ] **Step 5: 提交**

```bash
git add pkg/config/credentials.go pkg/config/credentials_test.go
git commit -m "feat(config): load/merge/save credentials.yaml as a separate key layer"
```

---

## Task 6: `Load()` 接入 credentials 合并

**Files:**
- Modify: `pkg/config/config.go:243-244`

- [ ] **Step 1: 接线**

`pkg/config/config.go` 第 243-244 行(`// Expand {env:VAR} ...` 与 `cfg.Providers = resolveProviders(cfg.Providers)`)之前插入 credentials 合并,改为:

```go
	// Fold TUI-managed credentials (~/.lcoder/credentials.yaml) into providers,
	// without overriding hand-written config.providers fields.
	if credPath := resolveCredentialsPath(); credPath != "" {
		if creds, err := LoadCredentials(credPath); err == nil {
			cfg.Providers = mergeCredentials(cfg.Providers, creds)
		} else {
			fmt.Fprintf(os.Stderr, "warning: 读取 credentials 失败,已忽略: %v\n", err)
		}
	}

	// Expand {env:VAR} references in provider connection settings.
	cfg.Providers = resolveProviders(cfg.Providers)
```

(`fmt` 与 `os` 已在 `config.go` import,无需新增。)

- [ ] **Step 2: 回归测试**

Run: `go test ./pkg/config/...`
Expected: PASS(已有用例 + Task4/5 用例全绿;合并逻辑已在 Task 5 单测覆盖)

- [ ] **Step 3: 构建确认**

Run: `go build ./...`
Expected: 无错误

- [ ] **Step 4: 提交**

```bash
git add pkg/config/config.go
git commit -m "feat(config): merge credentials.yaml into providers during Load"
```

---

## Task 7: Go 客户端 `RegisterProvider`

**Files:**
- Modify: `pkg/llm/client.go`(import `config`;新增方法)
- Test: `pkg/llm/register_test.go`

- [ ] **Step 1: 写失败测试**

创建 `pkg/llm/register_test.go`:

```go
package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
)

func TestRegisterProvider(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.RegisterProvider(context.Background(), "moonshot", config.ProviderConn{
		BaseURL: "https://api.moonshot.cn/v1", APIKey: "sk",
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if gotPath != "/v1/providers" {
		t.Fatalf("expected /v1/providers, got %s", gotPath)
	}
	if gotBody["name"] != "moonshot" || gotBody["api_key"] != "sk" {
		t.Fatalf("unexpected body: %+v", gotBody)
	}
}

func TestRegisterProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.RegisterProvider(context.Background(), "x", config.ProviderConn{}); err == nil {
		t.Fatal("expected error on 500")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/llm/ -run TestRegisterProvider -v`
Expected: FAIL —`c.RegisterProvider undefined`

- [ ] **Step 3: 实现**

`pkg/llm/client.go` 顶部 import 块(第 3-14 行)加入 `"github.com/lcoder/lcoder/pkg/config"`(放在 `pkg/models` 之前,按字母序)。

在 `ListModels` 方法之后(第 242 行后)新增:

```go
// RegisterProvider hot-registers a provider connection with the gateway via
// POST /v1/providers, so a newly added api key takes effect without restarting
// the gateway subprocess.
func (c *Client) RegisterProvider(ctx context.Context, name string, conn config.ProviderConn) error {
	body, err := json.Marshal(map[string]any{
		"name":     name,
		"base_url": conn.BaseURL,
		"api_key":  conn.APIKey,
		"route":    conn.Route,
		"headers":  conn.Headers,
	})
	if err != nil {
		return fmt.Errorf("marshal provider: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/providers", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gateway returned %d: %s", resp.StatusCode, string(data))
	}
	return nil
}
```

(`bytes`/`io`/`json`/`fmt`/`http`/`context` 均已在 `client.go` import。)

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/llm/ -run TestRegisterProvider -v`
Expected: PASS(2 passed)
Run: `go build ./...`
Expected: 无错误(确认 `llm`→`config` 无 import 环)

- [ ] **Step 5: 提交**

```bash
git add pkg/llm/client.go pkg/llm/register_test.go
git commit -m "feat(llm): add RegisterProvider client call for gateway hot-reload"
```

---

## Task 8: `.gitignore` 与文档同步

**Files:**
- Modify: `.gitignore`
- Modify: `configs/lcoder.yaml`

- [ ] **Step 1: 忽略 credentials**

`.gitignore` 在 `__pycache__/` 行之前新增两行:

```
credentials.yaml
.lcoder/credentials.yaml
```

- [ ] **Step 2: 文档同步**

`configs/lcoder.yaml` 的 providers 注释块(第 11-27 行)之后、`context:` 之前,新增说明注释:

```yaml
# API keys 不写在本文件。优先级:config.providers.<name>.api_key (手写,含 {env:VAR})
#   > ~/.lcoder/credentials.yaml (TUI 写入,0600) > 标准环境变量 (如 OPENAI_API_KEY)。
# credentials.yaml 示例:
#   openai:
#     api_key: sk-...
#   moonshot:
#     api_key: sk-...
#     base_url: https://api.moonshot.cn/v1
# provider 字段可省略 model 的归属:网关会按 model id 自行解析 provider 并路由。
```

- [ ] **Step 3: 验证 gitignore 生效**

Run: `git check-ignore -v credentials.yaml`
Expected: 输出匹配行(退出码 0),证明被忽略

- [ ] **Step 4: 提交**

```bash
git add .gitignore configs/lcoder.yaml
git commit -m "docs(config): ignore credentials.yaml and document key priority"
```

---

## 全量验证(计划一完成后)

- [ ] `go build ./...` 无错误
- [ ] `go test ./pkg/... ./cmd/...` 全绿
- [ ] `cd gateway && .venv/Scripts/python -m pytest` 全绿
- [ ] 手动冒烟:
  1. 写 `~/.lcoder/credentials.yaml` 含 `openai.api_key`,`config.yaml` 设 `model: gpt-4o`(provider 留空)→ 启动后能正常对话(网关按 model 解析 provider)。
  2. 网关运行中 `curl -X POST 127.0.0.1:8787/v1/providers -H 'Content-Type: application/json' -d '{"name":"moonshot","base_url":"https://api.moonshot.cn/v1","api_key":"sk"}'` 返回 `{"status":"ok"}`,随后用 moonshot 模型对话无需重启。

## 计划二预告(不在本计划范围)

TUI 部分(spec §8、§9)在本计划落地后单独成计划:`providerpanel.go`(选 provider→拉 `/v1/models` 选 model→输入 key)、`commands.go` 注册 `/provider`、首启无可用 key 时的向导、切换后调 `RegisterProvider` 与重算预算。届时基于本计划交付的真实接口(`BuiltinProviders`、`SaveCredentials`、`client.RegisterProvider`、`/v1/models`)编写,避免臆测 bubbletea 组件细节。

## 明确不做(YAGNI)

- OS keyring;model 元数据库搬进 Go;provider 健康探测/多 key 轮换;在 TurnRequest body 携带完整连接覆盖(用 reload 端点替代)。
