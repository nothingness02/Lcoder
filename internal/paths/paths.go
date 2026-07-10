// Package paths provides path construction helpers for Lcoder's standard
// directories and files.
package paths

import (
	"os"
	"path/filepath"
)

// HomeDir returns the user's home directory. If it cannot be determined, an
// empty string is returned.
func HomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// LCoderHome returns the path to a file or directory under ~/.lcoder. If the
// home directory is unavailable, it falls back to a path under the current
// working directory (./.lcoder).
func LCoderHome(parts ...string) string {
	home := HomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(append([]string{home, ".lcoder"}, parts...)...)
}
