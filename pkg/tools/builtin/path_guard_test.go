package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── ResolvePathAccess ─────────────────────────────────────────────────────

func TestResolvePathAccess_NormalRelative(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolvePathAccess("x.txt", dir, OpRead)
	if err != nil {
		t.Fatalf("normal relative path should succeed: %v", err)
	}
	want := filepath.Join(dir, "x.txt")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePathAccess_DotRelative(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolvePathAccess(".", dir, OpRead)
	if err != nil {
		t.Fatalf("dot path should succeed: %v", err)
	}
	want := filepath.Clean(dir)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePathAccess_AbsoluteInsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "sub", "file.go")
	got, err := ResolvePathAccess(abs, dir, OpRead)
	if err != nil {
		t.Fatalf("absolute path inside workspace should succeed: %v", err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("got %q, want %q", got, abs)
	}
}

func TestResolvePathAccess_AbsoluteOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	// Pick a path clearly outside the temp dir.
	abs := filepath.Join(string(filepath.Separator), "outside", "file.txt")
	if vol := filepath.VolumeName(dir); vol != "" {
		abs = vol + abs
	}

	got, err := ResolvePathAccess(abs, dir, OpRead)
	if err != nil {
		t.Fatalf("absolute path outside workspace should be allowed: %v", err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("got %q, want %q", got, abs)
	}
}

func TestResolvePathAccess_RelativeEscapeBlocked(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolvePathAccess("../../../etc/passwd", dir, OpRead)
	if err == nil {
		t.Fatal("relative path escaping workspace must be blocked")
	}
	var pg *PathGuardError
	if !asPathGuardError(err, &pg) {
		t.Fatalf("expected PathGuardError, got %T: %v", err, err)
	}
	if pg.Code != "PATH_OUTSIDE_WORKSPACE" {
		t.Fatalf("expected PATH_OUTSIDE_WORKSPACE, got %q", pg.Code)
	}
	if !strings.Contains(err.Error(), "<system>") {
		t.Fatalf("error should use <system> wrapper for LLM, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "../../../etc/passwd") {
		t.Fatalf("error should include the raw path, got %q", err.Error())
	}
}

func TestResolvePathAccess_RelativeEscapeReadError(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolvePathAccess("../secret/env", dir, OpRead)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Fatalf("read operation error should mention 'read', got %q", err.Error())
	}
}

func TestResolvePathAccess_RelativeEscapeWriteError(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolvePathAccess("../config/nginx.conf", dir, OpWrite)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "write or edit") {
		t.Fatalf("write operation error should mention 'write or edit', got %q", err.Error())
	}
}

func TestResolvePathAccess_RelativeEscapeSearchError(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolvePathAccess("../../../usr", dir, OpSearch)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Fatalf("search operation error should mention 'search', got %q", err.Error())
	}
}

// ── Sensitive-file detection ──────────────────────────────────────────────

func TestResolvePathAccess_DotEnvBlocked(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".env", "dir/.env", "subdir/.env"} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err == nil {
			t.Fatalf("%q must be blocked as sensitive", name)
		}
		var pg *PathGuardError
		if !asPathGuardError(err, &pg) || pg.Code != "PATH_SENSITIVE" {
			t.Fatalf("expected PATH_SENSITIVE for %q, got %v", name, err)
		}
	}
}

func TestResolvePathAccess_DotEnvExampleAllowed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".env.example", ".env.sample", ".env.template"} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err != nil {
			t.Fatalf("%q should be allowed (exempt), got: %v", name, err)
		}
	}
}

func TestResolvePathAccess_DotEnvPrefixedBlocked(t *testing.T) {
	dir := t.TempDir()
	// .env.production, .env.local etc. should be blocked.
	for _, name := range []string{".env.production", ".env.local", ".env.staging"} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err == nil {
			t.Fatalf("%q must be blocked", name)
		}
	}
}

func TestResolvePathAccess_SSHKeysBlocked(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"id_rsa",
		"id_ed25519",
		"id_ecdsa",
		"id_dsa",
		"dir/id_rsa",
		".ssh/id_ed25519",
	} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err == nil {
			t.Fatalf("%q must be blocked as sensitive", name)
		}
		var pg *PathGuardError
		if !asPathGuardError(err, &pg) || pg.Code != "PATH_SENSITIVE" {
			t.Fatalf("expected PATH_SENSITIVE for %q, got %v", name, err)
		}
	}
}

func TestResolvePathAccess_SSHPublicKeysAllowed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"id_rsa.pub",
		"id_ed25519.pub",
		"id_ecdsa.pub",
	} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err != nil {
			t.Fatalf("%q should be allowed (public key), got: %v", name, err)
		}
	}
}

func TestResolvePathAccess_SSHKeyVariantsBlocked(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"id_rsa.old",
		"id_rsa.bak",
		"id_rsa.pem",
		"id_rsa_backup",
		"id_ed25519-2024",
		"id_ecdsa.key",
	} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err == nil {
			t.Fatalf("%q must be blocked", name)
		}
	}
}

func TestResolvePathAccess_CredentialsBlocked(t *testing.T) {
	dir := t.TempDir()
	// "credentials" basename or path containing known credential paths.
	for _, name := range []string{
		"credentials",
		"credential",
		".aws/credentials",
		".gcp/credentials",
		".ssh/id_rsa",
	} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err == nil {
			t.Fatalf("%q must be blocked", name)
		}
	}
}

func TestResolvePathAccess_NormalFilesAllowed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"main.go",
		"config.yaml",
		"README.md",
		".gitignore",
		"package.json",
		"src/index.ts",
		"a.txt",
		"b.txt",
	} {
		_, err := ResolvePathAccess(name, dir, OpRead)
		if err != nil {
			t.Fatalf("%q should be allowed, got: %v", name, err)
		}
	}
}

func TestResolvePathAccess_WriteBlockedOnSensitive(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolvePathAccess("../.env", dir, OpWrite)
	if err == nil {
		t.Fatal("write to sensitive file must be blocked")
	}
	if !strings.Contains(err.Error(), "write or edit") {
		t.Fatalf("write error should mention 'write or edit', got %q", err.Error())
	}
}

// ── isWithinWorkspace ─────────────────────────────────────────────────────

func TestIsWithinWorkspace_Exact(t *testing.T) {
	if !isWithinWorkspace("/home/user/proj", "/home/user/proj") {
		t.Fatal("exact match should be within workspace")
	}
}

func TestIsWithinWorkspace_Subdirectory(t *testing.T) {
	if !isWithinWorkspace("/home/user/proj/src", "/home/user/proj") {
		t.Fatal("subdirectory should be within workspace")
	}
}

func TestIsWithinWorkspace_Outside(t *testing.T) {
	if isWithinWorkspace("/home/user/other", "/home/user/proj") {
		t.Fatal("sibling directory should not be within workspace")
	}
}

func TestIsWithinWorkspace_Parent(t *testing.T) {
	if isWithinWorkspace("/home/user", "/home/user/proj") {
		t.Fatal("parent directory should not be within workspace")
	}
}

func TestIsWithinWorkspace_PrefixAbuse(t *testing.T) {
	// "/proj-other" is NOT under "/proj" — prefix abuse must be caught.
	if isWithinWorkspace("/proj-other/src", "/proj") {
		t.Fatal("prefix abuse must be caught")
	}
	if isWithinWorkspace("/proj2", "/proj") {
		t.Fatal("prefix abuse must be caught")
	}
}

func TestIsWithinWorkspace_CleanPath(t *testing.T) {
	// Uncleaned paths are handled.
	if !isWithinWorkspace("/a/b/../b/c", "/a/b") {
		t.Fatal("cleaned path should be within workspace")
	}
}

func TestIsWithinWorkspace_WindowsCaseInsensitive(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("Windows-only test")
	}
	if !isWithinWorkspace("C:\\Users\\X\\Proj\\src", "c:\\users\\x\\proj") {
		t.Fatal("Windows paths should be case-insensitive")
	}
}

// ── isSensitivePath ───────────────────────────────────────────────────────

func TestIsSensitivePath_DotEnv(t *testing.T) {
	tests := []struct {
		path     string
		sensitive bool
	}{
		{".env", true},
		{"path/to/.env", true},
		{".env.production", true},
		{".env.local", true},
		{".env.example", false},
		{".env.sample", false},
		{".env.template", false},
		{"env", false},
		{".environment", false},
	}
	for _, tc := range tests {
		got := isSensitivePath(tc.path)
		if got != tc.sensitive {
			t.Errorf("isSensitivePath(%q) = %v, want %v", tc.path, got, tc.sensitive)
		}
	}
}

func TestIsSensitivePath_SSHKeys(t *testing.T) {
	sensitive := []string{
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		"id_rsa.old", "id_rsa.bak", "id_rsa.key", "id_rsa.pem",
		"id_ed25519-2024", "id_ecdsa_sk",
	}
	for _, p := range sensitive {
		if !isSensitivePath(p) {
			t.Errorf("%q should be sensitive", p)
		}
	}
	allowed := []string{
		"id_rsa.pub", "id_ed25519.pub", "id_ecdsa.pub",
		"id_rsafoo", // suffix 'f' is not '-'/'_'/'.' → not a variant
	}
	for _, p := range allowed {
		if isSensitivePath(p) {
			t.Errorf("%q should not be sensitive", p)
		}
	}
}

func TestIsSensitivePath_Credentials(t *testing.T) {
	sensitive := []string{
		"credentials",
		"credential",
		"/home/user/.aws/credentials",
		"/home/user/.gcp/credentials",
		"/root/.ssh/id_rsa",
	}
	for _, p := range sensitive {
		if !isSensitivePath(p) {
			t.Errorf("%q should be sensitive", p)
		}
	}
	allowed := []string{
		"credentials.json",
		"credentials.go",
		"aws-credentials.md",
	}
	for _, p := range allowed {
		if isSensitivePath(p) {
			t.Errorf("%q should not be sensitive", p)
		}
	}
}

// ── PathGuardError ────────────────────────────────────────────────────────

func TestPathGuardError_SensitiveCode(t *testing.T) {
	e := &PathGuardError{Code: "PATH_SENSITIVE", RawPath: ".env", Op: OpRead}
	if !strings.Contains(e.Error(), "<system>") || !strings.Contains(e.Error(), "sensitive-file") {
		t.Fatalf("unexpected error: %s", e.Error())
	}
}

func TestPathGuardError_OutsideCode(t *testing.T) {
	e := &PathGuardError{Code: "PATH_OUTSIDE_WORKSPACE", RawPath: "../etc", Resolved: "/etc", Op: OpRead}
	if !strings.Contains(e.Error(), "<system>") || !strings.Contains(e.Error(), "outside") {
		t.Fatalf("unexpected error: %s", e.Error())
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────

func asPathGuardError(err error, target **PathGuardError) bool {
	if pg, ok := err.(*PathGuardError); ok {
		*target = pg
		return true
	}
	return false
}
