package permissions

import "testing"

func TestPatternForCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"go test ./...", "go test *"},
		{"git status --short", "git status *"},
		{"ls -la", "ls *"},
		{"docker build -t x .", "docker build *"},
		{"", "*"},
	}
	for _, c := range cases {
		got := PatternForCommand(c.cmd)
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
}
