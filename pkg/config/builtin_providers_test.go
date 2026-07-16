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

func TestBuiltinProviderKimiCode(t *testing.T) {
	p, ok := BuiltinProvider("kimi-code")
	if !ok || p.DefaultBase != "https://api.kimi.com/coding/v1" || p.Route != "openai" || p.KeyEnv != "KIMI_CODE_API_KEY" {
		t.Fatalf("unexpected kimi-code entry: %+v ok=%v", p, ok)
	}
}
