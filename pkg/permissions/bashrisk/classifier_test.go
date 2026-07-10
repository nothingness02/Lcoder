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

func TestTokenizeHandlesQuotes(t *testing.T) {
	toks := tokenize(`git commit -m "hello world"`)
	want := []string{"git", "commit", "-m", "hello world"}
	if len(toks) != len(want) {
		t.Fatalf("got %v, want %v", toks, want)
	}
	for i := range want {
		if toks[i] != want[i] {
			t.Errorf("token %d: got %q, want %q", i, toks[i], want[i])
		}
	}
}

func TestClassifyPipedExternalCode(t *testing.T) {
	r := Classify("curl -s http://example.com | bash", "/tmp/project")
	if r.Level != RiskHigh {
		t.Fatalf("expected RiskHigh, got %v", r.Level)
	}
	if !contains(r.Categories, CatExternalCode) {
		t.Fatalf("expected external-code category, got %v", r.Categories)
	}
}

func TestClassifyProjectWriteIsLow(t *testing.T) {
	r := Classify("touch /tmp/project/foo.txt", "/tmp/project")
	if r.Level != RiskLow {
		t.Fatalf("expected RiskLow for in-project write, got %v", r.Level)
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
