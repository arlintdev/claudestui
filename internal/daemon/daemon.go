package daemon

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	idleTimeout = 60 * time.Second
	idleCheck   = 5 * time.Second
)

// ConfigDir returns the daemon config directory.
func ConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claudes")
}

// SocketPath returns the daemon socket path.
func SocketPath() string {
	return filepath.Join(ConfigDir(), "daemon.sock")
}

// PIDFilePath returns the daemon PID file path.
func PIDFilePath() string {
	return filepath.Join(ConfigDir(), "daemon.pid")
}

// Run starts the daemon in the foreground. Blocks until shutdown.
func Run() error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Write PID file
	pidPath := PIDFilePath()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	defer os.Remove(pidPath)

	// Clean up stale socket
	sockPath := SocketPath()
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	defer os.Remove(sockPath)

	// Make socket accessible
	os.Chmod(sockPath, 0600)

	pm := NewProcessManager()
	srv := NewServer(pm, listener)

	log.Printf("daemon started, pid=%d, socket=%s", os.Getpid(), sockPath)

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Start serving
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve()
	}()

	// Idle auto-exit goroutine
	idleDone := make(chan struct{})
	go func() {
		defer close(idleDone)
		for {
			time.Sleep(idleCheck)
			if pm.Count() == 0 && srv.ClientCount() == 0 {
				log.Printf("no processes and no clients, shutting down after idle timeout")
				time.Sleep(idleTimeout - idleCheck) // wait remainder
				// Re-check after full timeout
				if pm.Count() == 0 && srv.ClientCount() == 0 {
					log.Printf("still idle, exiting")
					srv.Close()
					return
				}
			}
		}
	}()

	// Wait for signal or idle exit
	select {
	case sig := <-sigCh:
		log.Printf("received signal %v, shutting down", sig)
	case err := <-serveDone:
		if err != nil {
			log.Printf("serve error: %v", err)
		}
	case <-idleDone:
	}

	// Graceful shutdown
	pm.ShutdownAll()
	log.Printf("daemon stopped")
	return nil
}

// cleanEnvForDaemon strips env vars that cause Claude Code to detect nesting.
func cleanEnvForDaemon(env []string) []string {
	strip := map[string]bool{
		"CLAUDE_CODE_ENTRYPOINT": true,
		"CLAUDECODE":             true,
	}
	var out []string
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		if strip[key] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// IsRunning checks if a daemon is already running by pinging the socket.
func IsRunning() bool {
	c, err := Dial(SocketPath())
	if err != nil {
		return false
	}
	defer c.Close()
	_, err = c.Ping()
	return err == nil
}

// EnsureRunning starts the daemon in the background if not already running.
// Returns once the daemon is accepting connections.
func EnsureRunning(exe string) error {
	if IsRunning() {
		return nil
	}

	// Fork daemon process with clean env (strip Claude nesting detection vars)
	cmd := fmt.Sprintf("%s daemon", exe)
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", cmd + " >/dev/null 2>&1"},
		&os.ProcAttr{
			Dir:   "/",
			Env:   cleanEnvForDaemon(os.Environ()),
			Files: []*os.File{os.Stdin, nil, nil},
			Sys:   &syscall.SysProcAttr{Setsid: true},
		})
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	proc.Release()

	// Wait for socket to appear (up to 3s)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if IsRunning() {
			return nil
		}
	}
	return fmt.Errorf("daemon did not start within 3s")
}
