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
