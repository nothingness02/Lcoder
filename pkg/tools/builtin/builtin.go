package builtin

import "github.com/lcoder/lcoder/pkg/tools"

// Factories returns all built-in tool factories. This is the single source of
// truth for the built-in tool set; init() registers it into
// tools.DefaultFactories.
func Factories() []tools.Factory {
	return []tools.Factory{
		NewRead,
		NewWrite,
		NewEdit,
		NewBash,
		NewLs,
		NewGrep,
		NewFind,
		NewTodoWrite,
	}
}
