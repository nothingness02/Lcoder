package llm

import (
	"context"
	"testing"
)

func TestModelWindowExactMatch(t *testing.T) {
	c := newTestClient()
	w, err := c.ModelWindow(context.Background(), "openai", "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 128000 {
		t.Fatalf("expected 128000, got %d", w)
	}
}

func TestModelWindowPrefixMatch(t *testing.T) {
	c := newTestClient()
	// Snapshot ids start with "claude-sonnet-4-5"/"claude-sonnet-4-6"; a shorter
	// query prefix-matches them (重生后 models.dev 不再单列 claude-sonnet-4)。
	w, _ := c.ModelWindow(context.Background(), "anthropic", "claude-sonnet-4")
	if w != 1000000 {
		t.Fatalf("expected 1000000 via prefix, got %d", w)
	}
}

func TestModelWindowProviderMismatch(t *testing.T) {
	c := newTestClient()
	w, _ := c.ModelWindow(context.Background(), "azure", "gpt-4o")
	if w != 0 {
		t.Fatalf("expected 0 for provider mismatch, got %d", w)
	}
}

func TestModelWindowUnknownModel(t *testing.T) {
	c := newTestClient()
	w, err := c.ModelWindow(context.Background(), "openai", "no-such-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 0 {
		t.Fatalf("expected 0 for unknown model, got %d", w)
	}
}
