// Package kimiweb supervises a `kimi web` child process, restarting it with
// exponential backoff when it exits unexpectedly.
package kimiweb

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"time"
)

// Supervisor runs and restarts the kimi web process.
type Supervisor struct {
	Bin  string
	Args []string
	// Env holds extra KEY=VALUE entries appended to the inherited environment.
	Env []string
}

// Run starts the process and blocks until ctx is cancelled (which kills the
// child) or the process can no longer be started.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := time.Second
	for {
		cmd := exec.CommandContext(ctx, s.Bin, s.Args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if len(s.Env) > 0 {
			cmd.Env = append(os.Environ(), s.Env...)
		}
		if err := cmd.Start(); err != nil {
			log.Printf("[kimiweb] failed to start %q: %v", s.Bin, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		log.Printf("[kimiweb] started: %s %v (pid %d)", s.Bin, s.Args, cmd.Process.Pid)
		backoff = time.Second

		err := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		log.Printf("[kimiweb] process exited: %v; restarting in %v", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// FreePort returns an available TCP port on the loopback interface.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

// WaitReady blocks until addr accepts TCP connections or the timeout passes.
func WaitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s to accept connections", addr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
