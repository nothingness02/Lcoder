package tui

import (
	"fmt"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// BenchmarkRebuildViewportManyMessages measures rebuildViewport with 2000
// conversation blocks (1000 user / 1000 assistant) at an 80x24 terminal.
// Baseline (2026-07-15): ~4.1 ms/op, ~1.65 MB/op, ~33.5k allocs/op.
func BenchmarkRebuildViewportManyMessages(b *testing.B) {
	bus := events.New()
	ag := &fakeAgent{}
	store := &fakeSessionStore{}
	sess := &fakeSession{ID: "bench"}
	m := NewModel(bus, ag, sess, store, ".", "bench", "openai/gpt-4o-mini", "dark", nil, nil, nil, nil, config.Config{}, nil, false, nil)
	defer m.Close()

	for i := 0; i < 1000; i++ {
		m.appendBlock(block{kind: components.BlockUser, raw: fmt.Sprintf("question %d", i)})
		m.appendBlock(block{kind: components.BlockAssistant, raw: fmt.Sprintf("answer %d with some **bold** text", i)})
	}
	m.width = 80
	m.height = 24
	m.updateSizes()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.rebuildViewport()
	}
}
