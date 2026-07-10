package main

import (
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

// toSandboxConfig translates the yaml-friendly config.SandboxConfig into the
// sandbox package's Config. projectRoot is the injected project root (the agent
// cwd, NOT the process CWD) used to resolve relative filesystem roots.
//
// Network default is mapped string -> bool with a default-deny posture: only the
// explicit "allow" enables DefaultAllow; "deny", empty, or any other value keep
// it false.
func toSandboxConfig(c config.SandboxConfig, projectRoot string) sandbox.Config {
	return sandbox.Config{
		Backend:      c.Backend,
		Runtime:      c.Runtime,
		Image:        c.Image,
		EnvAllowlist: c.EnvAllowlist,
		Network: sandbox.NetworkConfig{
			DefaultAllow: c.Network.Default == "allow",
			Allow:        c.Network.Allow,
		},
		Filesystem: sandbox.FilesystemConfig{
			Readable: c.Filesystem.Readable,
			Writable: c.Filesystem.Writable,
		},
		Limits: sandbox.ResourceLimits{
			MaxMemoryMB:    c.Limits.MaxMemoryMB,
			MaxCPUSeconds:  c.Limits.MaxCPUSeconds,
			MaxOutputBytes: c.Limits.MaxOutputBytes,
		},
		ProjectRoot: projectRoot,
	}
}
