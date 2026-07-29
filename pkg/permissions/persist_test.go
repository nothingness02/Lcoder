package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveProjectRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.yaml")
	if err := SaveRule(path, "bash", "go test *", Allow); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "go test *") {
		t.Fatalf("expected rule in file, got:\n%s", data)
	}
	if !strings.Contains(string(data), "allow") {
		t.Fatalf("expected allow decision in file, got:\n%s", data)
	}
}

func TestSaveRuleUpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.yaml")
	if err := SaveRule(path, "bash", "go test *", Allow); err != nil {
		t.Fatal(err)
	}
	if err := SaveRule(path, "bash", "go test *", Deny); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(DefaultConfig())
	if err := engine.LoadProjectRules(path); err != nil {
		t.Fatal(err)
	}
	if got := engine.Evaluate(Request{Tool: "bash", Command: "go test ./..."}); got != Deny {
		t.Fatalf("expected updated deny, got %v", got)
	}
}

// Patterns containing dots (file paths, python3 manage.py) must round-trip
// without corrupting the rules file (koanf key-path corruption regression).
func TestSaveRuleDottedPatternRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := SaveRule(path, "read", "src/main.go", Allow); err != nil {
		t.Fatal(err)
	}
	if err := SaveRule(path, "bash", "python3 manage.py *", Allow); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(DefaultConfig())
	if err := engine.LoadProjectRules(path); err != nil {
		t.Fatalf("reload after dotted saves: %v", err)
	}
	if got := engine.Evaluate(Request{Tool: "read", Path: "src/main.go"}); got != Allow {
		t.Fatalf("dotted path rule lost, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "bash", Command: "python3 manage.py runserver"}); got != Allow {
		t.Fatalf("dotted command rule lost, got %v", got)
	}
}

// A syntactically broken rules file must never be silently overwritten.
func TestSaveRuleRefusesBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte("permissions:\n  rules: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveRule(path, "bash", "*", Allow); err == nil {
		t.Fatal("expected refusal to overwrite a broken rules file")
	}
}
