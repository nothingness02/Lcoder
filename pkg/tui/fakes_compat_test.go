package tui

import "github.com/lcoder/lcoder/pkg/testutil"

// Compatibility aliases so existing TUI tests can keep using the short local
// names while the canonical fixtures live in pkg/testutil.
type fakeAgent = testutil.FakeAgent
type fakeSession = testutil.FakeSession
type fakeSessionStore = testutil.FakeSessionStore
