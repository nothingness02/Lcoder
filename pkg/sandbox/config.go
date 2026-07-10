package sandbox

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
)

// Config selects and parameterizes a sandbox backend.
type Config struct {
	Backend      string // "" or "passthrough" | "soft-limit" | "container"/"remote" (reserved)
	Runtime      string // container runtime executable, e.g. "docker" or "podman"
	Image        string // container image for the container backend
	EnvAllowlist []string
	Network      NetworkConfig
	Filesystem   FilesystemConfig
	Limits       ResourceLimits
	// ProjectRoot is the base for resolving relative filesystem roots. The CALLER
	// injects the project root here (NOT the process CWD) so the policy does not
	// drift across agents or launch directories (spec §8).
	ProjectRoot string
}

// NetworkConfig describes the network allowlist.
type NetworkConfig struct {
	DefaultAllow bool     // true = allow when no entry matches
	Allow        []string // "host:port" entries; host may be "*.example.com"; port "*"/empty = any
}

// FilesystemConfig describes allowed roots (relative to Config.ProjectRoot).
type FilesystemConfig struct {
	Readable []string
	Writable []string
}

var defaultEnvAllowlist = []string{"PATH", "HOME", "LANG", "SHELL"}

// New constructs a Sandbox for the given config. An empty backend defaults to
// passthrough. container/remote are reserved and return an explicit error.
func New(cfg Config) (Sandbox, error) {
	switch cfg.Backend {
	case "", "passthrough":
		return &passthrough{network: &passthroughNetwork{dialer: &net.Dialer{}}}, nil
	case "soft-limit":
		np, err := buildNetwork(cfg.Network)
		if err != nil {
			return nil, err
		}
		fs, err := buildFS(cfg.Filesystem, cfg.ProjectRoot)
		if err != nil {
			return nil, err
		}
		envAllow := cfg.EnvAllowlist
		if len(envAllow) == 0 {
			envAllow = defaultEnvAllowlist
		}
		return &softLimit{network: np, fs: fs, envAllow: envAllow}, nil
	case "container":
		return newContainerSandbox(cfg)
	case "remote":
		return nil, fmt.Errorf("sandbox backend %q not yet implemented (interface reserved)", cfg.Backend)
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q", cfg.Backend)
	}
}

func buildNetwork(c NetworkConfig) (*allowlistNetwork, error) {
	entries := make([]allowEntry, 0, len(c.Allow))
	for _, s := range c.Allow {
		e, err := parseAllowEntry(s)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return &allowlistNetwork{defaultAllow: c.DefaultAllow, entries: entries, dialer: &net.Dialer{}}, nil
}

func buildFS(c FilesystemConfig, root string) (*restrictedFS, error) {
	readable, err := resolveRoots(c.Readable, root)
	if err != nil {
		return nil, err
	}
	writable, err := resolveRoots(c.Writable, root)
	if err != nil {
		return nil, err
	}
	return &restrictedFS{readable: readable, writable: writable}, nil
}

// resolveRoots makes each root absolute against base, then normalizes it to its
// real physical path (so runtime checks use the same canonical form as targets).
func resolveRoots(roots []string, base string) ([]string, error) {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if !filepath.IsAbs(r) {
			r = filepath.Join(base, r)
		}
		real, err := resolvePath(r)
		if err != nil {
			return nil, err
		}
		out = append(out, real)
	}
	return out, nil
}

// parseAllowEntry parses "host:port", "host:*", or bare "host" (any port).
func parseAllowEntry(s string) (allowEntry, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return allowEntry{host: s, port: 0}, nil // bare host = any port
	}
	port := 0
	if portStr != "" && portStr != "*" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return allowEntry{}, fmt.Errorf("sandbox: bad port in allow entry %q: %w", s, err)
		}
		port = p
	}
	return allowEntry{host: host, port: port}, nil
}
