// Package fsutil provides filesystem helpers for Lcoder's private user data.
package fsutil

import (
	"os"
	"path/filepath"
)

// EnsurePrivateDir creates path with 0o700 permissions if it does not exist.
func EnsurePrivateDir(path string) error {
	return os.MkdirAll(path, 0o700)
}

// WritePrivateFile writes data to path, creating the parent directory as a
// private directory and setting the file permissions to 0o600.
func WritePrivateFile(path string, data []byte) error {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// OpenPrivateAppend opens path for append, creating the parent directory as a
// private directory and the file with 0o600 permissions.
func OpenPrivateAppend(path string) (*os.File, error) {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return f, nil
}
