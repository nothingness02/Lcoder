package llm

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestRegisterProvider(t *testing.T) {
	adapter := &fakeAdapter{events: []provider.Event{
		{Kind: provider.KindDone, Message: models.AssistantMessage("ok")},
	}}
	c := clientWithAdapter(adapter)

	err := c.RegisterProvider(context.Background(), "moonshot", config.ProviderConn{
		BaseURL: "https://api.moonshot.cn/v1", APIKey: "sk",
	})
	if err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}

	// A turn for the registered provider must now resolve and stream.
	stream, err := c.StreamTurn(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "moonshot", ID: "moonshot-v1-128k"},
	})
	if err != nil {
		t.Fatalf("StreamTurn after register: %v", err)
	}
	// Drain the channel.
	for range stream {
	}
	if adapter.calls != 1 {
		t.Fatalf("expected adapter to be invoked once, got %d", adapter.calls)
	}
}
