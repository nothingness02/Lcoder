// pkg/llm/client_engine_test.go
package llm_test

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestClientStreamMapsEvents(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := engine.New(cat)
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter {
		return scriptAdapter{[]provider.Event{
			{Kind: provider.KindStart},
			{Kind: provider.KindTextDelta, Delta: "hello"},
			{Kind: provider.KindDone, Message: models.AgentMessage{
				Role: models.RoleAssistant, Content: []models.ContentPart{models.TextContent{Text: "hello"}}}},
		}}
	})
	eng.RegisterProvider("openai", provider.Conn{Route: "openai"})
	c := llm.NewClient(eng)

	stream, err := c.StreamTurn(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotText string
	var sawDone bool
	for ev := range stream {
		switch ev.Kind {
		case provider.KindTextDelta:
			gotText += ev.Delta
		case provider.KindDone:
			sawDone = true
			if ev.Message.Text() != "hello" {
				t.Fatalf("final message wrong: %q", ev.Message.Text())
			}
		}
	}
	if gotText != "hello" || !sawDone {
		t.Fatalf("stream mapping wrong: text=%q done=%v", gotText, sawDone)
	}
}

// scriptAdapter is a local fake. (Mirrors engine.fakeAdapter; kept local to avoid exporting.)
type scriptAdapter struct{ events []provider.Event }

func (s scriptAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}
