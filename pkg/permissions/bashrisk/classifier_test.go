package bashrisk

import (
	"testing"
)

func TestClassifyReadOnly(t *testing.T) {
	r := Classify("ls -la", "/tmp/project")
	if r.Level != RiskNone {
		t.Fatalf("expected RiskNone, got %v", r.Level)
	}
}

func TestClassifyNetwork(t *testing.T) {
	r := Classify("curl http://example.com", "/tmp/project")
	if r.Level != RiskHigh {
		t.Fatalf("expected RiskHigh, got %v", r.Level)
	}
	if !contains(r.Categories, CatNetwork) {
		t.Fatalf("expected network category, got %v", r.Categories)
	}
}

func contains(cats []Category, c Category) bool {
	for _, x := range cats {
		if x == c {
			return true
		}
	}
	return false
}
