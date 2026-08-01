package llm

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
)

func TestStatus(t *testing.T) {
	client := newTestClient()
	if err := client.RegisterProvider(context.Background(), "openai", config.ProviderConn{}); err != nil {
		t.Fatal(err)
	}
	st := client.Status(context.Background())
	ps, ok := st.Providers["openai"]
	if !ok {
		t.Fatalf("openai missing from status: %+v", st.Providers)
	}
	if ps.Protocol != "openai-chat" {
		t.Fatalf("protocol = %q, want openai-chat", ps.Protocol)
	}
}

func TestListModels(t *testing.T) {
	client := newTestClient()
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	var found bool
	for _, m := range models {
		if m.ID == "gpt-4o" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gpt-4o in catalog models, got %v", models)
	}
}
