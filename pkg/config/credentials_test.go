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

func TestCredentialsPathExported(t *testing.T) {
	// Exported accessor must return the same value as the internal resolver.
	if CredentialsPath() != resolveCredentialsPath() {
		t.Fatalf("CredentialsPath()=%q != resolveCredentialsPath()=%q", CredentialsPath(), resolveCredentialsPath())
	}
}

func TestProviderHasKey(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConn{
		"openai": {APIKey: "sk-config"},
	}}

	// Key present in merged config.providers.
	if !ProviderHasKey(cfg, "openai") {
		t.Fatal("expected openai to have a key from config.providers")
	}

	// No config key, no env -> false.
	t.Setenv("ANTHROPIC_API_KEY", "")
	if ProviderHasKey(cfg, "anthropic") {
		t.Fatal("expected anthropic to lack a key")
	}

	// No config key, but standard env var set -> true.
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	if !ProviderHasKey(cfg, "anthropic") {
		t.Fatal("expected anthropic to have a key via env var")
	}

	// Unknown provider with no config/env -> false (no panic).
	if ProviderHasKey(cfg, "mystery") {
		t.Fatal("expected unknown provider to lack a key")
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

// setTempHome points os.UserHomeDir() at a fresh temp dir on every platform.
func setTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)        // unix
	t.Setenv("USERPROFILE", home) // windows
	return home
}

func TestSaveProviderSelectionPersistsThroughLoad(t *testing.T) {
	setTempHome(t)
	if err := SaveProviderSelection("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("SaveProviderSelection: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "deepseek" || cfg.Model != "deepseek-chat" {
		t.Fatalf("expected persisted provider/model, got %q/%q", cfg.Provider, cfg.Model)
	}
}

func TestSaveProviderSelectionPreservesExistingKeys(t *testing.T) {
	home := setTempHome(t)
	path := filepath.Join(home, ".lcoder", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := "provider: openai\nmodel: gpt-4o-mini\ntui:\n  theme: light\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := SaveProviderSelection("deepseek", "deepseek-chat"); err != nil {
		t.Fatalf("SaveProviderSelection: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "deepseek" || cfg.Model != "deepseek-chat" {
		t.Fatalf("expected updated provider/model, got %q/%q", cfg.Provider, cfg.Model)
	}
	if cfg.TUI.Theme != "light" {
		t.Fatalf("expected tui.theme preserved as 'light', got %q", cfg.TUI.Theme)
	}
}

func TestSaveThinkingPersistsThroughLoad(t *testing.T) {
	setTempHome(t)
	if err := SaveThinking("high"); err != nil {
		t.Fatalf("SaveThinking: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thinking != "high" {
		t.Fatalf("expected persisted thinking, got %q", cfg.Thinking)
	}
}

func TestSaveThinkingPreservesExistingKeys(t *testing.T) {
	home := setTempHome(t)
	path := filepath.Join(home, ".lcoder", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := "provider: openai\nmodel: gpt-4o-mini\ntui:\n  theme: light\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := SaveThinking("low"); err != nil {
		t.Fatalf("SaveThinking: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thinking != "low" {
		t.Fatalf("expected thinking 'low', got %q", cfg.Thinking)
	}
	if cfg.Provider != "openai" || cfg.Model != "gpt-4o-mini" {
		t.Fatalf("provider/model not preserved, got %q/%q", cfg.Provider, cfg.Model)
	}
	if cfg.TUI.Theme != "light" {
		t.Fatalf("expected tui.theme preserved as 'light', got %q", cfg.TUI.Theme)
	}
}

func TestMergeCredentialsPreservesResilienceFields(t *testing.T) {
	providers := map[string]ProviderConn{
		"p": {APIKey: "config-key", Protocol: "anthropic"},
	}
	creds := map[string]ProviderConn{
		"p": {APIKey: "tui-key", APIKeys: []string{"k1", "k2"}, Protocol: "openai-chat", MaxConcurrent: 4},
	}
	got := mergeCredentials(providers, creds)["p"]
	// config 手写字段优先
	if got.APIKey != "config-key" || got.Protocol != "anthropic" {
		t.Fatalf("config fields must win: %+v", got)
	}
	// 空缺字段从 credentials 补齐
	if len(got.APIKeys) != 2 || got.MaxConcurrent != 4 {
		t.Fatalf("credentials fields not merged: %+v", got)
	}
}
