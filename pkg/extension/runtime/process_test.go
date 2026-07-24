package runtime

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test; it is the child process invoked by
// TestStartProcessAppliesAllEnvKeys. It writes its EXT_* env vars to stderr
// and exits.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER") != "1" {
		return
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "EXT_") {
			os.Stderr.WriteString(e + "\n")
		}
	}
	os.Exit(0)
}

// TestStartProcessAppliesAllEnvKeys guards against the loop bug where each
// manifest env entry reset cmd.Env to os.Environ() plus only that entry,
// dropping all but the last key.
func TestStartProcessAppliesAllEnvKeys(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartProcess(Manifest{
		Name:    "envtest",
		Command: []string{exe, "-test.run", "TestHelperProcess"},
		Env:     map[string]string{"GO_WANT_HELPER": "1", "EXT_A": "1", "EXT_B": "2"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Wait for the stderr drain to capture the helper output.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := p.Stderr()
		if strings.Contains(s, "EXT_A=1") && strings.Contains(s, "EXT_B=2") {
			return // pass
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("helper env missing keys; stderr=%q", p.Stderr())
}
