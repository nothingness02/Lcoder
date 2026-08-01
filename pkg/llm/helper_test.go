package llm

import (
	"context"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// fakeAdapter serves in-package client tests. The first errUntil Stream calls
// fail with a provider.EventError carrying errCode (default "internal"); subsequent
// calls replay events. calls counts every Stream invocation.
type fakeAdapter struct {
	events   []provider.Event
	errUntil int
	errCode  string
	calls    int
}

func (f *fakeAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	f.calls++
	if f.calls <= f.errUntil {
		code := f.errCode
		if code == "" {
			code = "internal"
		}
		return nil, &provider.EventError{Code: code, Message: "injected"}
	}
	ch := make(chan provider.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// newTestClient builds a client over a fresh snapshot catalog with no adapter
// override (used for catalog-backed methods like ListModels/ModelWindow/Health).
func newTestClient() *Client {
	cat := catalog.New(catalog.Options{Refresh: false})
	return NewClient(engine.New(cat))
}

// clientWithAdapter builds a client whose turns are served by adapter.
func clientWithAdapter(adapter provider.Adapter) *Client {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := engine.New(cat)
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter { return adapter })
	return NewClient(eng)
}
