package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/extension/bridge"
	"github.com/lcoder/lcoder/pkg/extension/runtime"
	"github.com/lcoder/lcoder/pkg/session"
)

// startExtensions discovers, trusts, spawns, and bridges process-external
// extensions. It never fails the run: all problems degrade to warnings.
func startExtensions(cfg config.Config, cwd string, sess *session.Session, bus *events.Bus) (*runtime.Host, *bridge.Bridge) {
	disabled := make(map[string]bool, len(cfg.Extensions.Disabled))
	for _, name := range cfg.Extensions.Disabled {
		disabled[name] = true
	}
	timeout := time.Duration(cfg.Extensions.HookTimeoutMs) * time.Millisecond
	host := runtime.NewHost(runtime.HostOptions{Timeout: timeout})
	host.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "warning: %s\n", msg) }
	// The session handler is captured when each extension process is spawned,
	// so it must be installed before Load.
	host.SetSessionHandler(bridge.SessionHandler(sess, func(level, msg string) {
		fmt.Fprintf(os.Stderr, "[ext] %s: %s\n", level, msg)
	}))

	global, err := runtime.Discover(paths.LCoderHome("extensions"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: discover global extensions: %v\n", err)
	}
	project, err := runtime.Discover(filepath.Join(cwd, ".lcoder", "extensions"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: discover project extensions: %v\n", err)
	}

	var load []runtime.Manifest
	for _, m := range global {
		if !disabled[m.Name] {
			load = append(load, m)
		}
	}
	for _, m := range project {
		if disabled[m.Name] {
			continue
		}
		if trustProjectExtensions || (interactiveTrust() && stdinTrustPrompter(m.Name, m.Dir)) {
			load = append(load, m)
		} else {
			fmt.Fprintf(os.Stderr, "warning: skipping untrusted project extension %q\n", m.Name)
		}
	}
	if len(load) == 0 {
		return nil, nil
	}
	host.Load(load)
	b := bridge.New(host)
	b.SubscribeEvents(bus)
	return host, b
}

// interactiveTrust reports whether a stdin trust prompt is usable: TUI and
// one-shot modes yes; --json mode no (there is no user to answer).
func interactiveTrust() bool {
	return !jsonMode
}
