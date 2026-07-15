package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildInvocation(t *testing.T) {
	agent := Agent{
		Name:     "worker",
		Model:    "gpt-4o-mini",
		Provider: "openai",
		Mode:     "code",
	}
	args := buildInvocationArgs(agent, "do it")
	want := []string{"--json", "-p", "do it", "--model", "gpt-4o-mini", "--provider", "openai", "--mode", "code"}
	if len(args) != len(want) {
		t.Errorf("len(args) = %d, want %d", len(args), len(want))
	}
	for i, w := range want {
		if i >= len(args) || args[i] != w {
			t.Errorf("args[%d] = %q, want %q", i, args[i], w)
		}
	}
}

func TestRunSubprocessTimeout(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "block.go")
	if err := os.WriteFile(src, []byte(`package main
import "time"
func main() { time.Sleep(10 * time.Second) }
`), 0644); err != nil {
		t.Fatalf("write helper source: %v", err)
	}

	bin := filepath.Join(dir, "block")
	if os.PathSeparator == '\\' {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}

	ctx := context.Background()
	_, err := runSubprocess(ctx, bin, nil, "", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout message, got %v", err)
	}
}
