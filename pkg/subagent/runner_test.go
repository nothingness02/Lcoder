package subagent

import (
	"testing"
)

func TestDefaultRunnerValidateCWD(t *testing.T) {
	r := &DefaultRunner{projectRoot: "/tmp/proj"}
	_, err := r.validateCWD("/tmp/proj/../etc")
	if err == nil {
		t.Fatal("expected cwd outside project root error")
	}
}
