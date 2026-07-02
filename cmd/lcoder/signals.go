//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals that trigger a best-effort crash checkpoint
// before the process exits. On Unix-like systems we handle both SIGINT and
// SIGTERM; on Windows only os.Interrupt is available.
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
