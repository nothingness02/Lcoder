package tools

import "strings"

// AccessOperation describes how a tool call touches a resource.
// Mirrors Kimi Code's ToolFileAccessOperation plus the globally-exclusive
// 'all' variant (tool-access.ts).
type AccessOperation string

const (
	OpRead      AccessOperation = "read"
	OpWrite     AccessOperation = "write"
	OpReadWrite AccessOperation = "readwrite"
	OpSearch    AccessOperation = "search"
	// OpAll marks arbitrary side effects that cannot be expressed as a file
	// access; it conflicts with everything, including itself.
	OpAll AccessOperation = "all"
	// OpNone marks a call that touches no parent-side resource (e.g. a
	// subagent spawn, which runs in its own process/cwd context). It never
	// conflicts except with OpAll, so independent calls of this kind run in
	// parallel.
	OpNone AccessOperation = "none"
)

// ToolAccess declares one resource a tool call will touch. The agent's batch
// scheduler uses these declarations to decide which calls may overlap.
type ToolAccess struct {
	Op        AccessOperation
	Path      string // resolved absolute path; empty when Op == OpAll
	Recursive bool   // Path is a directory tree root
}

// AccessDeclarer is an optional interface a tool implements to declare the
// resources a call will touch, enabling fine-grained concurrent scheduling.
// Tools that do not implement it are treated as OpAll (fully serial).
type AccessDeclarer interface {
	DeclareAccesses(args map[string]any) []ToolAccess
}

// AccessesConflict reports whether any access in left conflicts with any in
// right.
func AccessesConflict(left, right []ToolAccess) bool {
	for _, l := range left {
		for _, r := range right {
			if AccessConflict(l, r) {
				return true
			}
		}
	}
	return false
}

// AccessConflict reports whether two accesses conflict: at least one writes
// (or is OpAll) and their paths overlap. OpNone touches nothing, so it only
// conflicts with OpAll (a fully serial tool must stay serial relative to it).
func AccessConflict(a, b ToolAccess) bool {
	if a.Op == OpAll || b.Op == OpAll {
		return true
	}
	if a.Op == OpNone || b.Op == OpNone {
		return false
	}
	if !accessWrites(a.Op) && !accessWrites(b.Op) {
		return false
	}
	return accessPathsOverlap(a, b)
}

func accessWrites(op AccessOperation) bool {
	return op == OpWrite || op == OpReadWrite
}

func accessPathsOverlap(a, b ToolAccess) bool {
	ap := normalizeAccessPath(a.Path)
	bp := normalizeAccessPath(b.Path)
	if ap == bp {
		return true
	}
	return (a.Recursive && strings.HasPrefix(bp, ap+"/")) ||
		(b.Recursive && strings.HasPrefix(ap, bp+"/"))
}

// normalizeAccessPath canonicalizes a path for conflict comparison:
// backslashes to slashes, duplicate slashes collapsed, lowercased (Windows
// and default macOS filesystems are case-insensitive), trailing slash
// stripped. Mirrors Kimi Code's normalizePath in tool-access.ts.
func normalizeAccessPath(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.ToLower(p)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
