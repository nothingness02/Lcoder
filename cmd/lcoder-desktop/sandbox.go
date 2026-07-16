package main

import (
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/sandbox"
)

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
