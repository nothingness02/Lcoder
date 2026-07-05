# Dynamic Model Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the static `MODEL_CATALOG` in the LLM Gateway with a `ModelRegistry` that merges a built-in fallback catalog, an optional JSON config file, and LiteLLM-based discovery for providers that have API keys configured.

**Architecture:** A single `ModelRegistry` class owns model loading and refresh logic. The FastAPI `/v1/models` endpoint asks the registry for the current list instead of returning a hard-coded array. Discovery uses LiteLLM's `models_by_provider` and `get_model_info` to derive capabilities and context windows only for providers whose API keys are present in the environment.

**Tech Stack:** Python 3.11+, FastAPI, LiteLLM, Pydantic, pytest.

---

## Task 1: Create the Model Registry Module

**Files:**
- Create: `gateway/lcoder_gateway/registry.py`

- [ ] **Step 1: Create `registry.py` with built-in catalog and registry class**

Create `gateway/lcoder_gateway/registry.py`:

```python
"""Dynamic model registry combining static catalog, config files, and LiteLLM discovery."""

from __future__ import annotations

import json
import logging
import os
from pathlib import Path
from typing import Any

import litellm

from .models import ModelInfo

logger = logging.getLogger(__name__)

# LiteLLM provider keys that map to gateway provider names.
LITELLM_PROVIDER_MAP: dict[str, str] = {
    "openai": "openai",
    "text-completion-openai": "openai",
    "anthropic": "anthropic",
    "deepseek": "deepseek",
}

# Capability flags returned by litellm.get_model_info mapped to gateway capability names.
CAPABILITY_MAP: dict[str, str] = {
    "supports_function_calling": "tools",
    "supports_vision": "vision",
    "supports_prompt_caching": "prompt_caching",
}

# Minimal static catalog used as a fallback when discovery is disabled or fails.
DEFAULT_CATALOG: list[ModelInfo] = [
    ModelInfo(
        id="gpt-4o",
        provider="openai",
        aliases=["4o"],
        capabilities=["tools", "vision", "streaming"],
        context_window=128_000,
    ),
    ModelInfo(
        id="gpt-4o-mini",
        provider="openai",
        aliases=["4o-mini"],
        capabilities=["tools", "vision", "streaming"],
        context_window=128_000,
    ),
    ModelInfo(
        id="claude-sonnet-4-20250514",
        provider="anthropic",
        aliases=["sonnet"],
        capabilities=["tools", "vision", "streaming"],
        context_window=200_000,
    ),
    ModelInfo(
        id="deepseek-chat",
        provider="deepseek",
        aliases=["deepseek"],
        capabilities=["tools", "streaming"],
        context_window=64_000,
    ),
    ModelInfo(
        id="deepseek-reasoner",
        provider="deepseek",
        aliases=["deepseek-r1"],
        capabilities=["streaming"],
        context_window=64_000,
    ),
]


def _configured_providers() -> list[str]:
    """Return providers for which an API key is present in the environment."""
    providers: list[str] = []
    if os.environ.get("OPENAI_API_KEY"):
        providers.append("openai")
    if os.environ.get("ANTHROPIC_API_KEY"):
        providers.append("anthropic")
    if os.environ.get("DEEPSEEK_API_KEY"):
        providers.append("deepseek")
    return providers


class ModelRegistry:
    """Registry that merges static catalog, JSON config, and LiteLLM discovery."""

    def __init__(
        self,
        config_path: str | Path | None = None,
        providers: list[str] | None = None,
        enable_discovery: bool = True,
    ) -> None:
        self.config_path = Path(config_path) if config_path else None
        self.providers = set(providers or _configured_providers())
        self.enable_discovery = enable_discovery
        self._models: dict[str, ModelInfo] = {}
        self.refresh()

    def refresh(self) -> None:
        """Rebuild the model catalog from all sources."""
        models: dict[str, ModelInfo] = {}

        # 1. Static fallback catalog.
        for model in DEFAULT_CATALOG:
            models[self._key(model.provider, model.id)] = model

        # 2. Optional JSON config file.
        if self.config_path:
            for model in self._load_config(self.config_path):
                models[self._key(model.provider, model.id)] = model

        # 3. LiteLLM discovery for configured providers.
        if self.enable_discovery:
            for model in self._discover_from_litellm():
                key = self._key(model.provider, model.id)
                if key not in models:
                    models[key] = model

        self._models = models

    def list_models(self) -> list[ModelInfo]:
        """Return all currently known models."""
        return list(self._models.values())

    def get_model(self, provider: str, model_id: str) -> ModelInfo | None:
        """Lookup a model by provider and id."""
        return self._models.get(self._key(provider, model_id))

    @staticmethod
    def _key(provider: str, model_id: str) -> str:
        return f"{provider}/{model_id}"

    def _load_config(self, path: Path) -> list[ModelInfo]:
        if not path.exists():
            logger.warning("Model registry config not found: %s", path)
            return []
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            logger.error("Invalid JSON in model registry config %s: %s", path, exc)
            return []
        models: list[ModelInfo] = []
        for idx, item in enumerate(data.get("models", [])):
            try:
                models.append(ModelInfo(**item))
            except Exception as exc:
                logger.warning("Skipping invalid model entry at index %s: %s", idx, exc)
        return models

    def _discover_from_litellm(self) -> list[ModelInfo]:
        """Discover models from LiteLLM for configured providers."""
        discovered: list[ModelInfo] = []
        for litellm_provider, model_ids in litellm.models_by_provider.items():
            provider = LITELLM_PROVIDER_MAP.get(litellm_provider)
            if provider is None or provider not in self.providers:
                continue
            for model_id in model_ids:
                info = self._safe_model_info(model_id)
                if info is None:
                    continue
                context_window = info.get("max_input_tokens") or info.get("max_tokens") or 0
                capabilities = ["streaming"]
                for flag, cap in CAPABILITY_MAP.items():
                    if info.get(flag):
                        capabilities.append(cap)
                discovered.append(
                    ModelInfo(
                        id=model_id,
                        provider=provider,
                        aliases=None,
                        capabilities=capabilities,
                        context_window=context_window,
                    )
                )
        return discovered

    def _safe_model_info(self, model_id: str) -> dict[str, Any] | None:
        try:
            return litellm.get_model_info(model_id)
        except Exception as exc:
            logger.debug("Unable to get model info for %s: %s", model_id, exc)
            return None
```

Run: `cd gateway && .venv/Scripts/python.exe -c "from lcoder_gateway.registry import ModelRegistry; r = ModelRegistry(enable_discovery=False); print(len(r.list_models()))"`
Expected output: `5`

- [ ] **Step 2: Commit**

```bash
cd gateway
git add lcoder_gateway/registry.py
git commit -m "feat(gateway): add ModelRegistry with static catalog and LiteLLM discovery"
```

---

## Task 2: Wire Registry into the FastAPI Server

**Files:**
- Modify: `gateway/lcoder_gateway/server.py`

- [ ] **Step 1: Replace static `MODEL_CATALOG` with a registry instance**

In `gateway/lcoder_gateway/server.py`:

1. Remove the hard-coded `MODEL_CATALOG` list.
2. Add import: `from .registry import ModelRegistry`.
3. Create a module-level registry instance after the `app` definition:

```python
from .registry import ModelRegistry

app = FastAPI(title="Lcoder LLM Gateway")
model_registry = ModelRegistry()
```

- [ ] **Step 2: Update `/v1/models` to use the registry**

Replace:

```python
@app.get("/v1/models")
async def list_models() -> list[ModelInfo]:
    return MODEL_CATALOG
```

With:

```python
@app.get("/v1/models")
async def list_models() -> list[ModelInfo]:
    return model_registry.list_models()
```

- [ ] **Step 3: Verify the server still starts**

Run: `cd gateway && timeout 5 .venv/Scripts/python.exe -m lcoder_gateway --port 8787 || true`
Expected: Server starts without import errors (timeout kills it after 5 seconds).

- [ ] **Step 4: Commit**

```bash
cd gateway
git add lcoder_gateway/server.py
git commit -m "feat(gateway): use ModelRegistry for /v1/models endpoint"
```

---

## Task 3: Add Environment-Based Configuration

**Files:**
- Modify: `gateway/lcoder_gateway/server.py`
- Modify: `gateway/lcoder_gateway/main.py`

- [ ] **Step 1: Read registry config path and discovery toggle from environment**

In `gateway/lcoder_gateway/server.py`, change the registry instantiation to read environment variables:

```python
import os

# ... existing imports ...
from .registry import ModelRegistry

app = FastAPI(title="Lcoder LLM Gateway")

_registry_config_path = os.environ.get("LCODER_MODEL_REGISTRY_PATH")
_registry_discovery = os.environ.get("LCODER_MODEL_DISCOVERY", "true").lower() in {"1", "true", "yes"}
model_registry = ModelRegistry(
    config_path=_registry_config_path,
    enable_discovery=_registry_discovery,
)
```

- [ ] **Step 2: Add CLI flag for `--model-registry-path` in `main.py`**

In `gateway/lcoder_gateway/main.py`, add an argument and set the environment variable before importing the app:

```python
def main() -> None:
    parser = argparse.ArgumentParser(description="Lcoder LLM Gateway")
    parser.add_argument("--host", default="127.0.0.1", help="Host to bind to")
    parser.add_argument("--port", type=int, default=8787, help="Port to bind to")
    parser.add_argument("--log-level", default="info", help="Uvicorn log level")
    parser.add_argument(
        "--model-registry-path",
        default=None,
        help="Path to a JSON model registry config file",
    )
    parser.add_argument(
        "--disable-model-discovery",
        action="store_true",
        help="Disable LiteLLM-based model discovery",
    )
    args = parser.parse_args()

    if args.model_registry_path:
        os.environ["LCODER_MODEL_REGISTRY_PATH"] = args.model_registry_path
    if args.disable_model_discovery:
        os.environ["LCODER_MODEL_DISCOVERY"] = "false"

    # Import app after env vars are set so registry picks them up.
    from .server import app

    config = uvicorn.Config(app, host=args.host, port=args.port, log_level=args.log_level)
    server = uvicorn.Server(config)
    server.run()
```

Because `server.py` was importing `app` at module load time in `main.py` (`from .server import app` was at the top), move that import inside `main()` so CLI flags are processed first.

- [ ] **Step 3: Test CLI flags**

Run: `cd gateway && .venv/Scripts/python.exe -m lcoder_gateway --disable-model-discovery --port 8787 & sleep 2; curl -s http://127.0.0.1:8787/v1/models | .venv/Scripts/python.exe -c "import sys,json; print(len(json.load(sys.stdin)))"; kill %1`
Expected output: `5` (only static catalog when discovery is disabled).

- [ ] **Step 4: Commit**

```bash
cd gateway
git add lcoder_gateway/server.py lcoder_gateway/main.py
git commit -m "feat(gateway): configure model registry via env vars and CLI flags"
```

---

## Task 4: Write Tests for the Registry

**Files:**
- Create: `gateway/tests/__init__.py`
- Create: `gateway/tests/test_registry.py`
- Modify: `gateway/pyproject.toml`

- [ ] **Step 1: Add pytest dev dependency**

In `gateway/pyproject.toml`, add a test dependency group:

```toml
[dependency-groups]
dev = ["pytest>=8.0.0"]
```

If your build backend does not support `dependency-groups`, use:

```toml
[project.optional-dependencies]
dev = ["pytest>=8.0.0"]
```

Then install it:

Run: `cd gateway && .venv/Scripts/python.exe -m pip install pytest`
Expected: pytest installs successfully.

- [ ] **Step 2: Create test package init**

Create `gateway/tests/__init__.py` as an empty file.

- [ ] **Step 3: Write tests**

Create `gateway/tests/test_registry.py`:

```python
from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from lcoder_gateway.registry import ModelRegistry


class TestStaticCatalog:
    def test_default_catalog_loaded_when_discovery_disabled(self) -> None:
        registry = ModelRegistry(enable_discovery=False)
        ids = {m.id for m in registry.list_models()}
        assert "gpt-4o" in ids
        assert "deepseek-chat" in ids


class TestConfigFile:
    def test_config_file_extends_catalog(self, tmp_path: Path) -> None:
        config = tmp_path / "models.json"
        config.write_text(
            json.dumps(
                {
                    "models": [
                        {
                            "id": "custom-model",
                            "provider": "openai",
                            "capabilities": ["streaming"],
                            "context_window": 4096,
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )
        registry = ModelRegistry(config_path=config, enable_discovery=False)
        model = registry.get_model("openai", "custom-model")
        assert model is not None
        assert model.context_window == 4096

    def test_invalid_config_entry_is_skipped(self, tmp_path: Path) -> None:
        config = tmp_path / "models.json"
        config.write_text(
            json.dumps({"models": [{"id": "bad", "provider": "openai"}]}),
            encoding="utf-8",
        )
        registry = ModelRegistry(config_path=config, enable_discovery=False)
        assert registry.get_model("openai", "bad") is None


class TestDiscovery:
    def test_discovery_respects_provider_filter(self, monkeypatch: pytest.MonkeyPatch) -> None:
        # Ensure only openai is considered configured.
        monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
        monkeypatch.delenv("DEEPSEEK_API_KEY", raising=False)
        monkeypatch.setenv("OPENAI_API_KEY", "test-key")

        registry = ModelRegistry(providers=["openai"], enable_discovery=True)
        models = registry.list_models()
        assert all(m.provider == "openai" for m in models if m.id not in {"gpt-4o", "gpt-4o-mini"})
```

- [ ] **Step 4: Run tests**

Run: `cd gateway && .venv/Scripts/python.exe -m pytest tests/test_registry.py -v`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
cd gateway
git add tests/ pyproject.toml
git commit -m "test(gateway): add ModelRegistry tests"
```

---

## Task 5: Update Gateway API Documentation

**Files:**
- Modify: `docs/gateway-api.md`

- [ ] **Step 1: Document dynamic discovery behavior**

In `docs/gateway-api.md`, replace the paragraph:

> 模型目录与价格表内置于 `gateway/lcoder_gateway/pricing.py`。

With:

> The model catalog is built dynamically at startup from three sources, in order of precedence:
> 1. Built-in fallback catalog (`gateway/lcoder_gateway/registry.py`).
> 2. Optional JSON config file pointed to by `LCODER_MODEL_REGISTRY_PATH` or `--model-registry-path`.
> 3. LiteLLM-based discovery for providers whose API keys are present (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`).
>
> Discovery can be disabled with `LCODER_MODEL_DISCOVERY=false` or `--disable-model-discovery`.

- [ ] **Step 2: Document the config file format**

Add a new section before section 7:

```markdown
## 7. Model Registry Configuration

A JSON config file can extend or override the built-in catalog:

```json
{
  "models": [
    {
      "id": "gpt-4o",
      "provider": "openai",
      "aliases": ["4o"],
      "capabilities": ["tools", "vision", "streaming"],
      "context_window": 128000
    }
  ]
}
```
```

Then renumber the following sections (Cost, Errors, Configuration) accordingly.

- [ ] **Step 3: Commit**

```bash
git add docs/gateway-api.md
git commit -m "docs(gateway): document dynamic model discovery and registry config"
```

---

## Task 6: Final Verification

**Files:**
- None

- [ ] **Step 1: Run the full test suite**

Run: `cd gateway && .venv/Scripts/python.exe -m pytest tests/ -v`
Expected: All tests pass.

- [ ] **Step 2: Start the gateway and query `/v1/models`**

Run:

```bash
cd gateway
.venv/Scripts/python.exe -m lcoder_gateway --port 8787 &
sleep 2
curl -s http://127.0.0.1:8787/v1/models | .venv/Scripts/python.exe -m json.tool
kill %1
```

Expected: JSON array of models, including static catalog entries and any discovered models for providers with API keys.

- [ ] **Step 3: Final commit if any changes remain**

```bash
git status
# Commit any remaining changes if necessary
```

---

## Self-Review Checklist

- **Spec coverage:** Dynamic discovery (`/v1/models` returns merged catalog) is implemented in Task 2. Config file support is in Task 1 and Task 3. Environment/CLI configuration is in Task 3. Tests are in Task 4. Docs are in Task 5.
- **Placeholder scan:** No TBD/TODO/fill-in-details placeholders. Every code block is complete.
- **Type consistency:** `ModelRegistry` constructor signature is consistent across creation, tests, and server wiring. No unused helper methods were introduced.
