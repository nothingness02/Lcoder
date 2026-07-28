package permissions

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestMatchPathDoubleStar(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		// "**" crosses any number of segments, including zero.
		{"**/.env", ".env", true},
		{"**/.env", "src/.env", true},
		{"**/.env", "a/b/c/.env", true},
		{"/etc/**", "/etc/passwd", true},
		{"/etc/**", "/etc/ssh/sshd_config", true},
		{"src/**/*.go", "src/main.go", true},
		{"src/**/*.go", "src/pkg/tools/x.go", true},
		// ...but it must not overreach.
		{"**/.env", ".env.backup", false},
		{"/etc/**", "/usr/etc/x", false},
		{"src/**/*.go", "other/main.go", false},
		// Single "*" stays within one segment.
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", false},
		{"src/*/main.go", "src/pkg/main.go", true},
		{"src/*/main.go", "src/pkg/tools/main.go", false},
		// Exact matches still work.
		{"main.go", "main.go", true},
		{"main.go", "other.go", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.target); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

// Lexical normalization closes the "./x" and "dir/../x" bypasses without
// touching the filesystem.
func TestMatchPathNormalization(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"**/.env", "./.env", true},
		{"**/.env", "sub/../.env", true},
		{"/etc/**", "/etc/../etc/passwd", true},
		{"src/*.go", "./src/main.go", true},
		{`src\*.go`, `src\main.go`, true}, // backslashes unified
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.target); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

func TestMatchPathWindowsCaseFolding(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case folding only applies on Windows")
	}
	if !MatchPath("**/.ENV", "src/.env") {
		t.Error("path matching should be case-insensitive on Windows")
	}
}

func TestMatchCommand(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"git status", "git status", true},
		{"git status *", "git status --short", true},
		// "*" crosses "/" so rules keep working when arguments contain paths.
		{"rm -rf *", "rm -rf /tmp/x", true},
		{"go test *", "go test ./...", true},
		{"sudo *", "sudo apt update", true},
		{"rm -rf *", "rm -rf", false},
		{"git status", "git status --short", false},
		// "**" behaves like "*" for commands.
		{"git push **", "git push origin main", true},
	}
	for _, c := range cases {
		if got := MatchCommand(c.pattern, c.target); got != c.want {
			t.Errorf("MatchCommand(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

// Engine-level: a deny rule with "**" blocks nested paths, and normalization
// stops the classic bypasses.
func TestEngineDenyWithDoubleStarAndNormalization(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"read": {"**/.env": Deny, "**": Allow},
		},
	})

	for _, path := range []string{".env", "src/.env", "./.env", "sub/../.env"} {
		if got := engine.Evaluate(Request{Tool: "read", Path: path}); got != Deny {
			t.Errorf("read %q: expected deny, got %v", path, got)
		}
	}
	if got := engine.Evaluate(Request{Tool: "read", Path: "src/main.go"}); got != Allow {
		t.Errorf("read of a normal file should be allowed, got %v", got)
	}
}

// Engine-level: command rules keep matching when arguments contain paths.
func TestEngineCommandRuleWithPathArguments(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"bash": {"rm -rf *": Deny, "*": Allow},
		},
	})

	if got := engine.Evaluate(Request{Tool: "bash", Command: "rm -rf /tmp/x"}); got != Deny {
		t.Fatalf("expected deny for rm -rf with path argument, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "bash", Command: "ls /tmp"}); got != Allow {
		t.Fatalf("expected allow for ls, got %v", got)
	}
}

// --- path-variant matching (engine with path context) ---

// The motivating bypass: a deny rule written with an absolute path must
// still catch the relative spellings the model actually passes.
func TestPathVariantsCloseRelativeBypass(t *testing.T) {
	dir := t.TempDir()
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"read": {filepath.Join(dir, ".env"): Deny, "**": Allow},
		},
	})
	engine.SetPathContext(dir, "")

	for _, spelling := range []string{".env", "./.env", "sub/../.env", filepath.Join(dir, ".env")} {
		if got := engine.Evaluate(Request{Tool: "read", Path: spelling}); got != Deny {
			t.Errorf("read %q: expected deny, got %v", spelling, got)
		}
	}
	if got := engine.Evaluate(Request{Tool: "read", Path: "main.go"}); got != Allow {
		t.Errorf("normal file should be allowed, got %v", got)
	}
}

// The mirror image: an allow rule written relative must also match the
// absolute spelling of a file inside the cwd.
func TestAllowRuleMatchesAbsoluteSpelling(t *testing.T) {
	dir := t.TempDir()
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"read": {"src/**": Allow},
		},
	})
	engine.SetPathContext(dir, "")

	decision, policy, _ := engine.EvaluateWithSource(Request{Tool: "read", Path: filepath.Join(dir, "src", "main.go")})
	if decision != Allow || policy != "user-rule" {
		t.Fatalf("absolute spelling should match the relative allow rule, got %v by %s", decision, policy)
	}
}

func TestHomeExpansion(t *testing.T) {
	home := t.TempDir()
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"read": {"~/.aws/**": Deny},
		},
	})
	engine.SetPathContext("", home)

	if got := engine.Evaluate(Request{Tool: "read", Path: filepath.Join(home, ".aws", "credentials")}); got != Deny {
		t.Errorf("absolute home path should match the ~ rule, got %v", got)
	}
	if got := engine.Evaluate(Request{Tool: "read", Path: "~/.aws/credentials"}); got != Deny {
		t.Errorf("~ spelling should match too, got %v", got)
	}
}

// Without path context the engine degrades to pure lexical matching.
func TestPathVariantsDegradeWithoutContext(t *testing.T) {
	engine := NewEngine(Config{
		Rules: map[string]RuleTable{
			"read": {"**/.env": Deny, "**": Allow},
		},
	})

	if got := engine.Evaluate(Request{Tool: "read", Path: "src/.env"}); got != Deny {
		t.Errorf("lexical ** matching still works without context, got %v", got)
	}
}

// Session approvals canonicalize path targets, so one approval covers all
// spellings of the same file — and only that file.
func TestSessionApprovalCanonicalizesPaths(t *testing.T) {
	dir := t.TempDir()
	engine := NewEngine(DefaultConfig())
	engine.SetPathContext(dir, "")
	engine.AddSessionRule("write", map[string]any{"path": "a.txt"})

	decision, policy, _ := engine.EvaluateWithSource(Request{Tool: "write", Path: filepath.Join(dir, "a.txt")})
	if decision != Allow || policy != "session-approval" {
		t.Fatalf("absolute spelling of the approved file should hit the session rule, got %v by %s", decision, policy)
	}
	if got := engine.Evaluate(Request{Tool: "write", Path: "b.txt"}); got != Deny {
		t.Fatalf("other files must not be covered, got %v", got)
	}
}
