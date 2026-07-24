package runtime

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Process is a running extension child process with its JSON-RPC connection.
type Process struct {
	Conn   *Conn
	cmd    *exec.Cmd
	mu     sync.Mutex
	stderr bytes.Buffer // bounded capture for diagnostics
}

// StartProcess spawns the extension described by m and returns it with the
// connection wired to its stdio. handler receives extension->host traffic.
func StartProcess(m Manifest, handler Handler) (*Process, error) {
	cmd := exec.Command(m.Command[0], m.Command[1:]...)
	cmd.Dir = m.Dir
	for k, v := range m.Env {
		cmd.Env = append(os.Environ(), k+"="+v)
	}
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
	go func() {
		_, _ = io.Copy(&lockedBuffer{mu: &p.mu, buf: &p.stderr}, io.LimitReader(stderr, 64*1024))
	}()
	p.Conn = NewConn(stdout, stdin, handler)
	return p, nil
}

// Stderr returns the captured stderr tail for diagnostics.
func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
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

type lockedBuffer struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
