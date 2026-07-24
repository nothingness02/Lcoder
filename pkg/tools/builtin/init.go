package builtin

import (
	"github.com/lcoder/lcoder/pkg/tools"
)

func init() {
	for _, f := range Factories() {
		tools.DefaultFactories.Register(f("").Definition().Name, f)
	}
}
