package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNopProviderPrefetch(t *testing.T) {
	entries, err := NopProvider.Prefetch(context.Background(), "query")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestNopProviderSyncTurn(t *testing.T) {
	require.NoError(t, NopProvider.SyncTurn(context.Background(), "hi", "hello"))
}

func TestNopProviderOnSessionEnd(t *testing.T) {
	require.NoError(t, NopProvider.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 3}))
}

func TestNopProviderHealthy(t *testing.T) {
	require.True(t, NopProvider.Healthy(context.Background()))
}
