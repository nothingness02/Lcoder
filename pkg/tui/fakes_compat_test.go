package tui

import (
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/testutil"
)

// Compatibility aliases so existing TUI tests can keep using the short local
// name while the canonical fixture lives in pkg/testutil.
type fakeAgent = testutil.FakeAgent

// newTestCoreModel builds a Model around a fake CoreAPI with the common test
// display/services defaults.
func newTestCoreModel(ag *testutil.FakeAgent) *Model {
	return NewModel(ag, host.Services{Bus: events.New()}, DisplayConfig{
		CWD:        ".",
		ModelRef:   "openai/gpt-4o-mini",
		ThemeStyle: "dark",
	})
}
