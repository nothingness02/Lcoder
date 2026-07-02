//go:build windows

package main

import "os"

// shutdownSignals are the signals that trigger a best-effort crash checkpoint
// before the process exits. Windows only supports os.Interrupt (Ctrl+C).
var shutdownSignals = []os.Signal{os.Interrupt}
