package goparser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/stretchr/testify/require"
)

func TestParseBasicSymbols(t *testing.T) {
	dir := t.TempDir()
	src := `package demo

// User represents a user.
type User struct {
    Name string
}

// NewUser creates a user.
func NewUser(name string) *User {
    return &User{Name: name}
}

// Greet greets someone.
func (u *User) Greet(msg string) string {
    return msg
}

const MaxCount = 10
var DefaultUser = NewUser("default")
`
	path := filepath.Join(dir, "demo.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	idx := NewIndexer(nil)
	require.NoError(t, idx.Update(context.Background(), dir))

	results, err := idx.Search(context.Background(), codeindex.Query{MaxResults: 20})
	require.NoError(t, err)

	names := make(map[string]codeindex.SymbolKind)
	for _, r := range results {
		names[r.Symbol.Name] = r.Symbol.Kind
	}
	require.Contains(t, names, "demo")
	require.Contains(t, names, "User")
	require.Contains(t, names, "NewUser")
	require.Contains(t, names, "User.Greet")
	require.Contains(t, names, "MaxCount")
	require.Contains(t, names, "DefaultUser")
}
