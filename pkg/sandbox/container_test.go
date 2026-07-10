package sandbox

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// fakeContainerRunner records the commands produced by the container backend and
// runs a harmless command (go env GOOS) so the tests do not need Docker.
type fakeContainerRunner struct {
	calls []*exec.Cmd
}

func (f *fakeContainerRunner) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", "env", "GOOS")
	cmd.Args = append([]string{name}, args...)
	f.calls = append(f.calls, cmd)
	return cmd
}

func indexOf(args []string, v string) int {
	for i, a := range args {
		if a == v {
			return i
		}
	}
	return -1
}

func TestContainerSandboxDefaultArgs(t *testing.T) {
	fake := &fakeContainerRunner{}
	orig := containerExecCommandContext
	containerExecCommandContext = fake.CommandContext
	defer func() { containerExecCommandContext = orig }()

	sb, err := New(Config{
		Backend:     "container",
		ProjectRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New container: %v", err)
	}

	_, _ = sb.Exec(context.Background(), ExecSpec{Command: "echo hi"})

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 container command, got %d", len(fake.calls))
	}
	args := fake.calls[0].Args
	wantPrefix := []string{"docker", "run", "--rm", "-i", "--network", "none"}
	if !slices.Equal(args[:len(wantPrefix)], wantPrefix) {
		t.Errorf("command prefix = %v, want %v", args[:len(wantPrefix)], wantPrefix)
	}
	w := indexOf(args, "-w")
	if w < 0 || w+1 >= len(args) || args[w+1] != "/workspace" {
		t.Errorf("missing -w /workspace in args: %v", args)
	}
	sh := indexOf(args, "sh")
	if sh < 0 || sh+2 >= len(args) || args[sh+1] != "-c" || args[sh+2] != "echo hi" {
		t.Errorf("expected sh -c echo hi in args, got %v", args)
	}
}

func TestContainerSandboxCustomRuntimeAndImage(t *testing.T) {
	fake := &fakeContainerRunner{}
	orig := containerExecCommandContext
	containerExecCommandContext = fake.CommandContext
	defer func() { containerExecCommandContext = orig }()

	sb, err := New(Config{
		Backend:     "container",
		Runtime:     "podman",
		Image:       "busybox",
		ProjectRoot: t.TempDir(),
		Network:     NetworkConfig{DefaultAllow: true},
	})
	if err != nil {
		t.Fatalf("New container: %v", err)
	}

	_, _ = sb.Exec(context.Background(), ExecSpec{Command: "true"})

	args := fake.calls[0].Args
	if args[0] != "podman" {
		t.Errorf("runtime = %q, want podman", args[0])
	}
	if !slices.Contains(args, "busybox") {
		t.Errorf("expected busybox image in args: %v", args)
	}
	if !slices.Contains(args, "bridge") {
		t.Errorf("expected bridge network in args: %v", args)
	}
}

func TestContainerSandboxMountsReadableWritable(t *testing.T) {
	fake := &fakeContainerRunner{}
	orig := containerExecCommandContext
	containerExecCommandContext = fake.CommandContext
	defer func() { containerExecCommandContext = orig }()

	root := t.TempDir()
	sb, err := New(Config{
		Backend:     "container",
		ProjectRoot: root,
		Filesystem: FilesystemConfig{
			Readable: []string{"src"},
			Writable: []string{"out"},
		},
	})
	if err != nil {
		t.Fatalf("New container: %v", err)
	}

	_, _ = sb.Exec(context.Background(), ExecSpec{Command: "true"})

	args := fake.calls[0].Args
	joined := strings.Join(args, " ")
	realRoot, err := resolvePath(root)
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}
	if !strings.Contains(joined, ":ro") {
		t.Errorf("expected a read-only mount in args: %v", args)
	}
	if strings.Contains(joined, realRoot+":/workspace:ro") {
		t.Errorf("workspace mount should not be read-only: %v", args)
	}
	if !strings.Contains(joined, realRoot+":/workspace") {
		t.Errorf("expected workspace mount for project root in args: %v", args)
	}
}

func TestContainerSandboxNetworkPolicyDeniesByDefault(t *testing.T) {
	fake := &fakeContainerRunner{}
	orig := containerExecCommandContext
	containerExecCommandContext = fake.CommandContext
	defer func() { containerExecCommandContext = orig }()

	sb, err := New(Config{
		Backend:     "container",
		ProjectRoot: t.TempDir(),
		Network:     NetworkConfig{DefaultAllow: false},
	})
	if err != nil {
		t.Fatalf("New container: %v", err)
	}

	_, err = sb.Network().DialContext(context.Background(), "tcp", "example.com:80")
	if err == nil {
		t.Fatal("expected network dial to be denied")
	}
}
