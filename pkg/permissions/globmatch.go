package permissions

import (
	"path/filepath"
	"runtime"
	"strings"
)

// This file implements the glob matching shared by permission rules and mode
// rules (pkg/agent). The two target kinds are matched differently:
//
//   - Paths (file tools): both sides are normalized lexically ("./" and ".."
//     segments cleaned, slashes unified, case-folded on Windows) and matched
//     segment-wise — "*" and "?" stay within one segment, "**" crosses any
//     number of segments including zero.
//   - Commands (bash): the whole string is matched with "*" and "?" crossing
//     any character. Command arguments routinely contain paths, and a "*"
//     that stops at "/" would silently fail to match "rm -rf /tmp/x".
//
// Normalization is lexical only — it closes "./x" and "dir/../x" bypasses
// without touching the filesystem (no symlink resolution, no home expansion).

// MatchPath reports whether a path rule pattern matches a path target.
func MatchPath(pattern, target string) bool {
	return matchSegments(splitPath(normalizePath(pattern)), splitPath(normalizePath(target)))
}

// normalizePath lexically normalizes a path for rule matching.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = filepath.Clean(p)
	p = filepath.ToSlash(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func splitPath(p string) []string {
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}

// matchSegments matches pattern segments against target segments using the
// classic two-pointer wildcard algorithm with backtracking on "**".
func matchSegments(pattern, target []string) bool {
	star := -1 // index of the last seen "**" in pattern, -1 = none
	starTarget := 0
	i, j := 0, 0
	for j < len(target) {
		if i < len(pattern) && pattern[i] == "**" {
			star = i
			starTarget = j
			i++
			continue
		}
		if i < len(pattern) && segmentMatch(pattern[i], target[j]) {
			i++
			j++
			continue
		}
		if star >= 0 {
			starTarget++
			j = starTarget
			i = star + 1
			continue
		}
		return false
	}
	for i < len(pattern) && pattern[i] == "**" {
		i++
	}
	return i == len(pattern)
}

// segmentMatch matches a single path segment: "*" and "?" do not cross
// separators because the segment contains none.
func segmentMatch(pattern, segment string) bool {
	matched, err := filepath.Match(pattern, segment)
	return err == nil && matched
}

// MatchCommand reports whether a command rule pattern matches a command
// string. "*" and "?" cross any character including path separators; "**"
// behaves like "*".
func MatchCommand(pattern, target string) bool {
	// The placeholder trick makes both slash kinds ordinary characters so "*"
	// matches across them (same approach as the ultra-destructive matcher).
	// Windows command lines routinely contain backslash paths.
	const placeholder = "\x00"
	p := filepath.ToSlash(strings.ReplaceAll(pattern, "**", "*"))
	target = filepath.ToSlash(target)
	matched, err := filepath.Match(
		strings.ReplaceAll(p, "/", placeholder),
		strings.ReplaceAll(target, "/", placeholder))
	return err == nil && matched
}

// --- path variants (kimi-code path-glob-match.ts ported to Go) ---
//
// Rules and model arguments spell paths differently: a rule may say
// "/repo/.env" while the model passes ".env", "./.env", or "sub/../.env".
// Rather than forcing one canonical form (which would lose the matching
// intent of one side's spelling), both sides are expanded into a set of
// equivalent structural spellings and every pair is tried. Slash and case
// differences are handled inside MatchPath's normalizePath, so the variant
// set only captures structural differences: "./" stripping, "~" expansion,
// and resolution against the engine's cwd.

// MatchPathVariants reports whether any structural spelling of the pattern
// matches any structural spelling of the target. Without path context (no
// cwd/homeDir set) it degrades to plain MatchPath on the raw spellings.
func (e *Engine) MatchPathVariants(pattern, target string) bool {
	for _, pv := range e.pathVariants(pattern) {
		for _, tv := range e.pathVariants(target) {
			if MatchPath(pv, tv) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) pathVariants(p string) []string {
	variants := []string{p, stripLeadingDotSlash(p)}
	if c := e.canonicalPath(p); c != "" {
		variants = append(variants, c)
	}
	seen := make(map[string]bool, len(variants))
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// canonicalPath expands "~" and resolves p against the engine's cwd,
// lexically (no symlink resolution, no filesystem access). It returns ""
// when p is relative and the engine has no cwd, meaning the canonical
// variant is unavailable and matching stays on the raw spellings.
func (e *Engine) canonicalPath(p string) string {
	expanded := expandHome(p, e.homeDir)
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	if e.cwd == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(e.cwd, expanded))
}

func expandHome(p, homeDir string) string {
	if homeDir == "" {
		return p
	}
	if p == "~" {
		return homeDir
	}
	if strings.HasPrefix(p, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(p, `~\`)) {
		return filepath.Join(homeDir, p[2:])
	}
	return p
}

func stripLeadingDotSlash(p string) string {
	if strings.HasPrefix(p, "./") {
		return p[2:]
	}
	if runtime.GOOS == "windows" && strings.HasPrefix(p, `.\`) {
		return p[2:]
	}
	return p
}
