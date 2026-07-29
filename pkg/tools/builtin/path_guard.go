package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathOperation describes the kind of file access, used to produce accurate
// LLM-facing error messages. Mirrors Kimi Code's PathAccessOperation.
type PathOperation string

const (
	OpRead   PathOperation = "read"
	OpWrite  PathOperation = "write"
	OpSearch PathOperation = "search"
)

// PathGuardError is a structured, LLM-actionable path security error.
// The Error() message is designed to be fed directly to the model as a
// tool result so it can self-correct without guessing.
type PathGuardError struct {
	Code     string        // "PATH_SENSITIVE" | "PATH_OUTSIDE_WORKSPACE"
	RawPath  string
	Resolved string
	Op       PathOperation
}

func (e *PathGuardError) Error() string {
	switch e.Code {
	case "PATH_SENSITIVE":
		return fmt.Sprintf(
			`<system>Path security blocked "%s": matches sensitive-file pattern `+
				`(.env / SSH key / credentials). Automated %s access to secrets is blocked `+
				`to protect sensitive data. If you need values from this file, ask the user directly.</system>`,
			e.RawPath, e.opVerb())
	case "PATH_OUTSIDE_WORKSPACE":
		return fmt.Sprintf(
			`<system>Path security blocked "%s": resolved to "%s" which is outside `+
				`the working directory. To %s files outside the workspace, `+
				`provide an absolute path.</system>`,
			e.RawPath, e.Resolved, e.opVerb())
	default:
		return fmt.Sprintf(`<system>Path security blocked "%s"</system>`, e.RawPath)
	}
}

func (e *PathGuardError) opVerb() string {
	switch e.Op {
	case OpWrite:
		return "write or edit"
	case OpSearch:
		return "search"
	default:
		return "read"
	}
}

// ResolvePathAccess is the single entry point for all file-tool path
// resolution. It canonicalizes rawPath against cwd, then applies two
// hard security checks before any I/O occurs:
//
//  1. Sensitive-file detection (.env, SSH keys, credentials) → deny
//  2. Workspace boundary: relative paths that escape cwd → deny;
//     absolute paths outside cwd are allowed.
//
// Mirrors Kimi Code's resolvePathAccess() in path-access.ts.
// Callers that receive a PathGuardError should surface its Error() text
// directly to the LLM as tool output.
func ResolvePathAccess(rawPath, cwd string, op PathOperation) (string, error) {
	resolved := resolveInCwd(cwd, rawPath)

	// ① Sensitive files: unconditionally deny — no tool, no mode, no user
	//    rule can override this. Mirrors Kimi Code's checkSensitive.
	if isSensitivePath(resolved) {
		return "", &PathGuardError{
			Code:     "PATH_SENSITIVE",
			RawPath:  rawPath,
			Resolved: resolved,
			Op:       op,
		}
	}

	// ② Workspace boundary: relative paths that escape cwd are denied.
	//    Absolute paths outside the workspace are allowed (the user or the
	//    permission engine decides whether the operation is authorized).
	if !isWithinWorkspace(resolved, cwd) {
		if !filepath.IsAbs(rawPath) && !strings.HasPrefix(rawPath, "~") {
			return "", &PathGuardError{
				Code:     "PATH_OUTSIDE_WORKSPACE",
				RawPath:  rawPath,
				Resolved: resolved,
				Op:       op,
			}
		}
	}

	return resolved, nil
}

// isWithinWorkspace reports whether candidate (already canonical) is under
// base. Pure lexical comparison — no symlink resolution. Mirrors Kimi Code's
// isWithinDirectory().
func isWithinWorkspace(candidate, base string) bool {
	nc := filepath.Clean(candidate)
	nb := filepath.Clean(base)

	// Case-insensitive comparison on Windows.
	if os.PathSeparator == '\\' {
		nc = strings.ToLower(nc)
		nb = strings.ToLower(nb)
	}

	if nc == nb {
		return true
	}
	prefix := nb
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(nc, prefix)
}

// ── Sensitive-file detection ──────────────────────────────────────────────
//
// Mirrors Kimi Code's sensitive.ts. The pattern list is intentionally small
// to avoid false positives; files matching any of these are blocked from all
// tools (read, write, edit, grep, find, ls).

// isSensitivePath returns true when path matches a sensitive-file pattern
// (.env, SSH private key, credentials file).
func isSensitivePath(path string) bool {
	name := strings.ToLower(filepath.Base(path))

	// .env (exempt .env.example / .env.sample / .env.template).
	if name == ".env" {
		return true
	}
	if strings.HasPrefix(name, ".env.") {
		return !envExemptions[name]
	}

	// SSH private keys (exempt .pub public keys).
	for _, key := range sshKeyNames {
		if name == key {
			return true
		}
		// .pub is public — allow.
		if name == key+".pub" {
			return false
		}
		// Variants: id_rsa.old, id_rsa.bak, id_rsa-2024, id_rsa_sk
		for _, suffix := range sensitiveSuffixes {
			if name == key+suffix {
				return true
			}
		}
		if strings.HasPrefix(name, key+"-") || strings.HasPrefix(name, key+"_") {
			return true
		}
	}

	// Credential files.
	if name == "credentials" || name == "credential" {
		return true
	}

	// Path patterns: ~/.aws/credentials, ~/.gcp/credentials, ~/.ssh/
	lower := strings.ToLower(path)
	for _, sp := range sensitivePathPatterns {
		if strings.Contains(lower, sp) {
			return true
		}
	}

	return false
}

var sshKeyNames = []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"}

var sensitiveSuffixes = []string{
	".bak", ".backup", ".copy", ".disabled",
	".key", ".old", ".orig", ".pem", ".save", ".tmp",
}

var envExemptions = map[string]bool{
	".env.example":  true,
	".env.sample":   true,
	".env.template": true,
}

var sensitivePathPatterns = []string{
	string(filepath.Separator) + ".aws" + string(filepath.Separator) + "credentials",
	string(filepath.Separator) + ".gcp" + string(filepath.Separator) + "credentials",
	string(filepath.Separator) + ".ssh" + string(filepath.Separator),
}
