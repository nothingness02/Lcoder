package codeindex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSymbolKindValues(t *testing.T) {
	require.Equal(t, SymbolKind("function"), SymbolKindFunc)
	require.Equal(t, SymbolKind("method"), SymbolKindMethod)
}
