package runtime

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// stderrTailLimit is the maximum number of stderr bytes retained for
// diagnostics; only the most recent bytes are kept.
const stderrTailLimit = 64 * 1024

// Process is a running extension child process with its JSON-RPC connection.
type Process struct {
	Conn   *Conn
	cmd    *exec.Cmd
	stderr tailBuffer // bounded capture for diagnostics
}

// StartProcess spawns the extension described by m and returns it with the
// connection wired to its stdio. handler receives extension->host traffic.
func StartProcess(m Manifest, handler Handler) (*Process, error) {
	cmd := exec.Command(m.Command[0], m.Command[1:]...)
	cmd.Dir = m.Dir
	env := os.Environ()
	for k, v := range m.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("extension %s stdin: %w", m.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("extension %s stdout: %w", m.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("extension %s stderr: %w", m.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start extension %s: %w", m.Name, err)
	}
	p := &Process{cmd: cmd}
	// Drain stderr for the life of the process so a verbose child never
	// blocks on a full pipe; only the tail is retained.
	go func() {
		_, _ = io.Copy(&p.stderr, stderr)
	}()
	p.Conn = NewConn(stdout, stdin, handler)
	return p, nil
}

// Stderr returns the most recent stderr bytes (up to 64KB) for diagnostics.
func (p *Process) Stderr() string {
	return p.stderr.String()
}

// Close shuts the connection first — Conn.Close closes the child's stdin,
// which is what makes a well-behaved child exit on its own — then waits for
// the process and kills it after 5s if it has not exited. The stdio pipes are
// owned by the Conn and must not be closed again here.
func (p *Process) Close() error {
	_ = p.Conn.Close()
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	return nil
}

// tailBuffer is an io.Writer that keeps only the most recent N bytes.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > stderrTailLimit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-stderrTailLimit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
