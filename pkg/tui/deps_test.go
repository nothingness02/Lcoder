package tui

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDependencyDirection pins the UI/agent protocol boundary: the TUI's
// non-test code must depend on pkg/agentapi (and host.Services), never on the
// agent implementation or the persistence internals it used to reach into.
// Checked via `go list` on the package's direct imports so it runs in CI with
// the standard toolchain.
func TestDependencyDirection(t *testing.T) {
	forbidden := []string{
		"github.com/lcoder/lcoder/pkg/agent",
		"github.com/lcoder/lcoder/pkg/session",
		"github.com/lcoder/lcoder/pkg/contextmgr",
		"github.com/lcoder/lcoder/pkg/checkpoint",
	}

	out, err := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, imp := range strings.Split(string(out), "\n") {
		imp = strings.TrimSpace(imp)
		for _, bad := range forbidden {
			if imp == bad || strings.HasPrefix(imp, bad+"/") {
				t.Errorf("pkg/tui must not import %s directly (go through agentapi.CoreAPI)", imp)
			}
		}
	}
}
