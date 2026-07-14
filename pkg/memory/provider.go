package memory

import "context"

// MemoryInjector recalls relevant memories before each turn.
type MemoryInjector interface {
	Prefetch(ctx context.Context, query string) error
}

// MemorySink extends MemoryInjector with turn/session persistence hooks.
type MemorySink interface {
	MemoryInjector
	SyncTurn(ctx context.Context, user, assistant string) error
	OnSessionEnd(ctx context.Context, summary SessionSummary) error
}

// Provider supplies memory-related context for a conversation session.
type Provider interface {
	// Prefetch returns relevant memory snippets for the given query.
	Prefetch(ctx context.Context, query string) ([]string, error)
	// SyncTurn records a completed user/assistant turn.
	SyncTurn(ctx context.Context, user, assistant string) error
	// OnSessionEnd is called when the session ends with a short summary.
	OnSessionEnd(ctx context.Context, summary SessionSummary) error
	// Healthy reports whether the provider is ready to serve requests.
	Healthy(ctx context.Context) bool
}

// SessionSummary is a lightweight summary passed to the provider at session end.
type SessionSummary struct {
	SessionID string // unique session identifier
	TurnCount int    // number of completed turns
}

// Compile-time assertion that nopProvider implements Provider.
var _ Provider = nopProvider{}

type nopProvider struct{}

func (nopProvider) Prefetch(ctx context.Context, query string) ([]string, error) { return nil, nil }
func (nopProvider) SyncTurn(ctx context.Context, user, assistant string) error    { return nil }
func (nopProvider) OnSessionEnd(ctx context.Context, summary SessionSummary) error { return nil }
func (nopProvider) Healthy(ctx context.Context) bool                              { return true }

// NopProvider is a no-op Provider usable as a safe default.
var NopProvider Provider = nopProvider{}
