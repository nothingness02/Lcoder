package tui

import "testing"

func TestRegisterExtensionCommandConflict(t *testing.T) {
	// commandRegistry is package-global and shared across tests; snapshot and
	// restore it so extension registrations here do not leak into other tests.
	saved := commandRegistry
	t.Cleanup(func() { commandRegistry = saved })

	// Built-in name conflicts are rejected.
	if err := RegisterExtensionCommand("help", "x", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected conflict error for built-in name")
	}
	// Alias conflicts are rejected too.
	if err := RegisterExtensionCommand("q", "x", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected conflict error for alias")
	}
	// A fresh name registers; duplicate registration is rejected.
	name := "testextcmd"
	if err := RegisterExtensionCommand(name, "desc", "/testextcmd", func(args string) string { return "ran:" + args }); err != nil {
		t.Fatal(err)
	}
	if err := RegisterExtensionCommand(name, "desc2", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected conflict error for duplicate extension command")
	}
	// It dispatches: find the entry and run its handler against a minimal model.
	for _, e := range commandRegistry {
		if e.Name == name {
			m := &Model{}
			_ = e.Handler(m, "args")
			if len(m.blocks) != 1 || m.blocks[0].raw != "ran:args" {
				t.Fatalf("expected handler output as system block, got %+v", m.blocks)
			}
			return
		}
	}
	t.Fatal("extension command not in registry")
}

func TestRegisterExtensionCommandValidation(t *testing.T) {
	saved := commandRegistry
	t.Cleanup(func() { commandRegistry = saved })

	// Mixed-case names normalize to lowercase on registration.
	if err := RegisterExtensionCommand("TESTEXTCMD2", "desc", "/testextcmd2", func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range commandRegistry {
		if e.Name == "testextcmd2" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected upper-case name to register lowercased")
	}

	// Empty (or whitespace-only) names are rejected.
	if err := RegisterExtensionCommand("  ", "desc", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected error for empty name")
	}

	// Nil invoke is rejected.
	if err := RegisterExtensionCommand("testextcmd3", "desc", "x", nil); err == nil {
		t.Fatal("expected error for nil invoke")
	}
}
