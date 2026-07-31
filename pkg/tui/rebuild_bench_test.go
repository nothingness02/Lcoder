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
// After the zero-alloc virtual content build (2026-07-30, i9-14900HX):
// ~2.2 ms/op, ~1.1 MB/op, ~26.4k allocs/op.
func BenchmarkRebuildViewportManyMessages(b *testing.B) {
	bus := events.New()
	ag := &fakeAgent{}
	store := &fakeSessionStore{}
	sess := &fakeSession{ID: "bench"}
	m := NewModel(bus, ag, sess, store, ".", "bench", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
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

// BenchmarkRebuildViewport10k measures the frame cost with a 10k-block
// history: layout walks every block while only the visible window renders.
// Blocks are assigned directly (not appendBlock) to keep setup O(n).
// Baseline (2026-07-30, i9-14900HX): ~10.9 ms/op — under the 33ms frame
// budget the scheduler enforces.
func BenchmarkRebuildViewport10k(b *testing.B) {
	bus := events.New()
	ag := &fakeAgent{}
	store := &fakeSessionStore{}
	sess := &fakeSession{ID: "bench"}
	m := NewModel(bus, ag, sess, store, ".", "bench", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
	defer m.Close()

	for i := 0; i < 5000; i++ {
		m.blocks = append(m.blocks,
			block{kind: components.BlockUser, raw: fmt.Sprintf("question %d", i)},
			block{kind: components.BlockAssistant, raw: fmt.Sprintf("answer %d with some **bold** text", i)},
		)
	}
	m.components = componentsFromBlocks(m.blocks)
	m.width = 80
	m.height = 24
	m.updateSizes()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.rebuildViewport()
	}
}

// BenchmarkStreamPatch measures one streaming delta landing on the in-flight
// assistant block with a warm scheduler (coalesced path — the common case
// during a delta burst), over a 2000-block history.
// Baseline (2026-07-30, i9-14900HX): ~129 ns/op.
func BenchmarkStreamPatch(b *testing.B) {
	bus := events.New()
	ag := &fakeAgent{}
	store := &fakeSessionStore{}
	sess := &fakeSession{ID: "bench"}
	m := NewModel(bus, ag, sess, store, ".", "bench", "openai/gpt-4o-mini", "dark", nil, nil, nil, config.Config{}, nil, false, nil)
	defer m.Close()

	for i := 0; i < 1000; i++ {
		m.blocks = append(m.blocks,
			block{kind: components.BlockUser, raw: fmt.Sprintf("question %d", i)},
			block{kind: components.BlockAssistant, raw: fmt.Sprintf("answer %d", i)},
		)
	}
	m.components = componentsFromBlocks(m.blocks)
	m.width = 80
	m.height = 24
	m.updateSizes()

	m.streaming = true
	m.streamMsgID = "live"
	m.appendBlock(block{kind: components.BlockAssistant, id: "live", raw: ""})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.patchAssistant(fmt.Sprintf("live answer delta %d with **bold** and `code`", i))
	}
}
