package builtin

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInCwdRelative(t *testing.T) {
	got := resolveInCwd("/proj", "x.txt")
	want := filepath.Clean("/proj/x.txt")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveInCwdAbsolute(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "abs", "x.txt")
	if vol := filepath.VolumeName("C:\\"); vol != "" {
		abs = "C:" + abs // ensure absolute on Windows
	}
	got := resolveInCwd("/proj", abs)
	if got != filepath.Clean(abs) {
		t.Fatalf("got %q", got)
	}
}

func TestWalkErrorLogNotice(t *testing.T) {
	var w walkErrorLog
	if got := w.notice(); got != "" {
		t.Fatalf("empty log should produce no notice, got %q", got)
	}
	for i := 0; i < 5; i++ {
		w.record(fmt.Sprintf("p%d", i), fmt.Errorf("err %d", i))
	}
	got := w.notice()
	if !strings.Contains(got, "5 path(s) unreadable") {
		t.Fatalf("notice should carry the exact count, got %q", got)
	}
	if strings.Contains(got, "p3") || strings.Contains(got, "p4") {
		t.Fatalf("examples should be capped at 3, got %q", got)
	}
}
