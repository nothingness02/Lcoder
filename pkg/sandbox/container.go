package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// containerSandbox runs commands inside a Docker/Podman container. It is a
// subprocess-plane backend: the in-process network/filesystem policies are still
// enforced for tools that use them directly, while commands execute in a
// container with bind mounts derived from the filesystem config.
type containerSandbox struct {
	runtime       string
	image         string
	networkMode   string
	mounts        []Mount
	envAllow      []string
	networkPolicy *allowlistNetwork
	fsPolicy      *restrictedFS
}

// newContainerSandbox builds a container backend from config.
func newContainerSandbox(cfg Config) (*containerSandbox, error) {
	runtime := cfg.Runtime
	if runtime == "" {
		runtime = "docker"
	}
	image := cfg.Image
	if image == "" {
		image = "alpine:latest"
	}

	networkMode := "none"
	if cfg.Network.DefaultAllow {
		networkMode = "bridge"
	}

	np, err := buildNetwork(cfg.Network)
	if err != nil {
		return nil, err
	}
	fs, err := buildFS(cfg.Filesystem, cfg.ProjectRoot)
	if err != nil {
		return nil, err
	}
	mounts, err := buildContainerMounts(cfg.Filesystem, cfg.ProjectRoot)
	if err != nil {
		return nil, err
	}

	envAllow := cfg.EnvAllowlist
	if len(envAllow) == 0 {
		envAllow = defaultEnvAllowlist
	}

	return &containerSandbox{
		runtime:       runtime,
		image:         image,
		networkMode:   networkMode,
		mounts:        mounts,
		envAllow:      envAllow,
		networkPolicy: np,
		fsPolicy:      fs,
	}, nil
}

func (c *containerSandbox) Name() string                 { return "container" }
func (c *containerSandbox) Network() NetworkPolicy       { return c.networkPolicy }
func (c *containerSandbox) Filesystem() FilesystemPolicy { return c.fsPolicy }

func (c *containerSandbox) Exec(ctx context.Context, spec ExecSpec) (ExecResult, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"run", "--rm", "-i", "--network", c.networkMode}
	for _, m := range c.mounts {
		mountArg := fmt.Sprintf("%s:%s", m.Source, m.Target)
		if m.ReadOnly {
			mountArg += ":ro"
		}
		args = append(args, "-v", mountArg)
	}
	for _, e := range scrubEnv(spec.Env, c.envAllow) {
		args = append(args, "-e", e)
	}

	// The project root is mounted at /workspace; run commands there.
	args = append(args, "-w", "/workspace")
	args = append(args, c.image, "sh", "-c", spec.Command)

	cmd := containerExecCommandContext(runCtx, c.runtime, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	execErr := cmd.Run()
	// CommandContext kills the process on deadline; distinguish a timeout from a
	// user cancellation.
	timedOut := runCtx.Err() == context.DeadlineExceeded && ctx.Err() != context.Canceled

	res := ExecResult{
		Stdout:   capOutput(stdout.String(), spec.Limits.MaxOutputBytes),
		Stderr:   capOutput(stderr.String(), spec.Limits.MaxOutputBytes),
		ExitCode: exitCode(execErr),
		TimedOut: timedOut,
	}
	if timedOut {
		return res, nil
	}
	return res, execErr
}

// buildContainerMounts produces bind mounts for the container. The project root
// is always mounted at /workspace. Additional readable/writable roots are mounted
// at their host absolute path so absolute paths inside commands continue to work.
func buildContainerMounts(c FilesystemConfig, projectRoot string) ([]Mount, error) {
	rootReal, err := resolvePath(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve project root: %w", err)
	}

	seen := map[string]bool{rootReal: true}
	mounts := []Mount{{Source: rootReal, Target: "/workspace", ReadOnly: false}}

	add := func(paths []string, readOnly bool) error {
		for _, p := range paths {
			if !filepath.IsAbs(p) {
				p = filepath.Join(projectRoot, p)
			}
			real, err := resolvePath(p)
			if err != nil {
				return fmt.Errorf("sandbox: resolve mount %q: %w", p, err)
			}
			if seen[real] {
				continue
			}
			seen[real] = true
			mounts = append(mounts, Mount{Source: real, Target: real, ReadOnly: readOnly})
		}
		return nil
	}
	if err := add(c.Readable, true); err != nil {
		return nil, err
	}
	if err := add(c.Writable, false); err != nil {
		return nil, err
	}
	return mounts, nil
}

var (
	_ Sandbox = (*containerSandbox)(nil)

	// containerExecCommandContext is overridable in tests to avoid requiring a real
	// Docker/Podman daemon.
	containerExecCommandContext = exec.CommandContext
)
