package tools

import (
	"testing"
)

func TestString(t *testing.T) {
	args := map[string]any{"path": "main.go", "limit": float64(10)}
	if got := String(args, "path", ""); got != "main.go" {
		t.Fatalf("String path = %q, want main.go", got)
	}
	if got := String(args, "missing", "default"); got != "default" {
		t.Fatalf("String missing = %q, want default", got)
	}
}

func TestInt(t *testing.T) {
	args := map[string]any{"a": float64(7), "b": int64(8)}
	if got := Int(args, "a", 0); got != 7 {
		t.Fatalf("Int a = %d, want 7", got)
	}
	if got := Int(args, "b", 0); got != 8 {
		t.Fatalf("Int b = %d, want 8", got)
	}
	if got := Int(args, "missing", 5); got != 5 {
		t.Fatalf("Int missing = %d, want 5", got)
	}
}

func TestStringSlice(t *testing.T) {
	args := map[string]any{"outs": []any{"a.txt", "b.txt"}}
	got := StringSlice(args, "outs")
	want := []string{"a.txt", "b.txt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("StringSlice = %v, want %v", got, want)
	}
}

func TestNormalizeArgs(t *testing.T) {
	a := map[string]any{"path": "main.go", "offset": float64(1)}
	b := map[string]any{"offset": float64(1), "path": "main.go"}
	if NormalizeArgs(a) != NormalizeArgs(b) {
		t.Fatalf("NormalizeArgs should be order-independent: %q vs %q", NormalizeArgs(a), NormalizeArgs(b))
	}
}
