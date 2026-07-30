package tools

import "testing"

func TestAccessConflict(t *testing.T) {
	read := func(p string) ToolAccess { return ToolAccess{Op: OpRead, Path: p} }
	write := func(p string) ToolAccess { return ToolAccess{Op: OpWrite, Path: p} }
	rw := func(p string) ToolAccess { return ToolAccess{Op: OpReadWrite, Path: p} }
	search := func(p string) ToolAccess { return ToolAccess{Op: OpSearch, Path: p, Recursive: true} }
	all := ToolAccess{Op: OpAll}

	tests := []struct {
		name string
		a, b ToolAccess
		want bool
	}{
		{"read+read same file", read("/w/a.go"), read("/w/a.go"), false},
		{"read+write same file", read("/w/a.go"), write("/w/a.go"), true},
		{"write+write diff files", write("/w/a.go"), write("/w/b.go"), false},
		{"readwrite+read same file", rw("/w/a.go"), read("/w/a.go"), true},
		{"search+read under tree", search("/w"), read("/w/sub/a.go"), false},
		{"search+write under tree", search("/w"), write("/w/sub/a.go"), true},
		{"write tree + read under", ToolAccess{Op: OpWrite, Path: "/w", Recursive: true}, read("/w/a.go"), true},
		{"case-insensitive same file", read("/w/A.go"), write("/w/a.go"), true},
		{"backslash vs slash", read(`C:\w\a.go`), write("C:/w/a.go"), true},
		{"trailing slash dir", search("/w/"), write("/w/a.go"), true},
		{"all vs read", all, read("/w/a.go"), true},
		{"all vs all", all, all, true},
		{"non-recursive dir vs child", ToolAccess{Op: OpWrite, Path: "/w"}, read("/w/a.go"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AccessConflict(tt.a, tt.b); got != tt.want {
				t.Fatalf("AccessConflict(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestAccessesConflictSets(t *testing.T) {
	left := []ToolAccess{{Op: OpRead, Path: "/w/a.go"}}
	right := []ToolAccess{{Op: OpWrite, Path: "/w/b.go"}, {Op: OpWrite, Path: "/w/a.go"}}
	if !AccessesConflict(left, right) {
		t.Fatal("expected conflict via second access in set")
	}
	if AccessesConflict(left, []ToolAccess{{Op: OpRead, Path: "/w/a.go"}}) {
		t.Fatal("read+read must not conflict")
	}
	if AccessesConflict(nil, []ToolAccess{{Op: OpAll}}) {
		t.Fatal("empty set conflicts with nothing")
	}
}
