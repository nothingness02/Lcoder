package builtin

import (
	"reflect"
	"testing"

	"github.com/lcoder/lcoder/pkg/tools"
)

func TestBuiltinAccessDeclarations(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		name string
		tool tools.Executable
		args map[string]any
		want []tools.ToolAccess
	}{
		{"read", NewRead(cwd), map[string]any{"path": "a.go"},
			[]tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(cwd, "a.go")}}},
		{"write", NewWrite(cwd), map[string]any{"path": "a.go"},
			[]tools.ToolAccess{{Op: tools.OpWrite, Path: resolveInCwd(cwd, "a.go")}}},
		{"edit", NewEdit(cwd), map[string]any{"path": "a.go"},
			[]tools.ToolAccess{{Op: tools.OpReadWrite, Path: resolveInCwd(cwd, "a.go")}}},
		{"grep default root", NewGrep(cwd), map[string]any{"pattern": "x"},
			[]tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(cwd, cwd), Recursive: true}}},
		{"grep explicit dir", NewGrep(cwd), map[string]any{"pattern": "x", "path": "sub"},
			[]tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(cwd, "sub"), Recursive: true}}},
		{"find", NewFind(cwd), map[string]any{"pattern": "*.go"},
			[]tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(cwd, cwd), Recursive: true}}},
		{"ls", NewLs(cwd), map[string]any{},
			[]tools.ToolAccess{{Op: tools.OpRead, Path: resolveInCwd(cwd, cwd), Recursive: true}}},
		{"bash", NewBash(cwd), map[string]any{"command": "go build ./..."},
			[]tools.ToolAccess{{Op: tools.OpAll}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declarer, ok := tt.tool.(tools.AccessDeclarer)
			if !ok {
				t.Fatalf("%T does not implement tools.AccessDeclarer", tt.tool)
			}
			if got := declarer.DeclareAccesses(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DeclareAccesses(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// 未实现接口的工具(以 TodoWrite 为代表)在 executor 侧按 OpAll 处理。
func TestToolWithoutDeclarationIsNotDeclarer(t *testing.T) {
	if _, ok := NewTodoWrite(t.TempDir()).(tools.AccessDeclarer); ok {
		t.Fatal("TodoWrite must NOT implement AccessDeclarer; default is OpAll")
	}
}
