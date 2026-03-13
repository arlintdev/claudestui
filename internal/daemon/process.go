package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const ringBufferSize = 256 * 1024 // 256KB scrollback

// subscriber receives output and exit events for a process.
type subscriber struct {
	ch chan Event
}

// Process manages a single child process with a PTY and ring buffer.
type Process struct {
	mu         sync.Mutex
	id         string
	cmd        *exec.Cmd
	ptmx       *os.File
	pid        int
	running    bool
	exitCode   int
	ring       *ringBuffer
	subs       map[*subscriber]struct{}
	subsMu     sync.Mutex
	stopOnce   sync.Once
	exitCh     chan struct{} // closed when process exits
}

// ringBuffer is a fixed-size circular byte buffer for scrollback.
type ringBuffer struct {
	buf  []byte
	size int
	pos  int // next write position
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size), size: size}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= r.size {
		// Data larger than buffer — keep only the tail
		copy(r.buf, p[n-r.size:])
		r.pos = 0
		r.full = true
		return n, nil
	}
	// How much fits before wrap?
	space := r.size - r.pos
	if n <= space {
		copy(r.buf[r.pos:], p)
	} else {
		copy(r.buf[r.pos:], p[:space])
		copy(r.buf, p[space:])
	}
	r.pos = (r.pos + n) % r.size
	if !r.full && r.pos < n {
		// We wrapped at least once
		r.full = true
	}
	return n, nil
}

// Bytes returns the buffered content in order.
func (r *ringBuffer) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	out := make([]byte, r.size)
	// Oldest data starts at r.pos (wrap point)
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}

// ProcessManager tracks all managed child processes.
type ProcessManager struct {
	mu        sync.RWMutex
	processes map[string]*Process
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*Process),
	}
}

func (pm *ProcessManager) Launch(params LaunchParams) (*Process, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// If an old (dead) process exists with the same ID, clean it up
	if old, exists := pm.processes[params.ID]; exists {
		old.mu.Lock()
		running := old.running
		old.mu.Unlock()
		if running {
			pm.mu.Unlock()
			pm.Kill(params.ID)
			pm.mu.Lock()
		}
		// Close subscribers and remove
		old.subsMu.Lock()
		for sub := range old.subs {
			close(sub.ch)
			delete(old.subs, sub)
		}
		old.subsMu.Unlock()
		delete(pm.processes, params.ID)
	}

	// Parse command into shell invocation
	cmd := exec.Command("sh", "-c", params.Command)
	cmd.Dir = params.Dir
	cmd.Env = cleanEnv(os.Environ())

	// Start with PTY
	winSize := &pty.Winsize{
		Cols: uint16(params.Cols),
		Rows: uint16(params.Rows),
	}
	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	proc := &Process{
		id:      params.ID,
		cmd:     cmd,
		ptmx:    ptmx,
		pid:     cmd.Process.Pid,
		running: true,
		ring:    newRingBuffer(ringBufferSize),
		subs:    make(map[*subscriber]struct{}),
		exitCh:  make(chan struct{}),
	}

	pm.processes[params.ID] = proc

	// Read PTY output → ring buffer + fan out to subscribers
	go proc.readLoop()

	// Wait for exit
	go proc.waitLoop()

	return proc, nil
}

func (p *Process) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			data := buf[:n]

			p.mu.Lock()
			p.ring.Write(data)
			p.mu.Unlock()

			// Fan out to subscribers
			p.fanOut(data)
		}
		if err != nil {
			return
		}
	}
}

func (p *Process) fanOut(data []byte) {
	b64 := base64.StdEncoding.EncodeToString(data)
	evtData, _ := json.Marshal(OutputEventData{
		ID:   p.id,
		Data: b64,
	})
	evt := Event{Event: EventOutput, Data: evtData}

	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	for sub := range p.subs {
		select {
		case sub.ch <- evt:
		default:
			// Slow subscriber, drop
		}
	}
}

func (p *Process) waitLoop() {
	err := p.cmd.Wait()

	p.mu.Lock()
	p.running = false
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			p.exitCode = exitErr.ExitCode()
		} else {
			p.exitCode = -1
		}
	}
	p.mu.Unlock()

	// Close PTY
	p.ptmx.Close()

	// Notify subscribers of exit
	evtData, _ := json.Marshal(ExitEventData{
		ID:       p.id,
		ExitCode: p.exitCode,
	})
	evt := Event{Event: EventExit, Data: evtData}

	p.subsMu.Lock()
	for sub := range p.subs {
		select {
		case sub.ch <- evt:
		default:
		}
	}
	p.subsMu.Unlock()

	close(p.exitCh)
}

func (pm *ProcessManager) Kill(id string) error {
	pm.mu.RLock()
	proc, ok := pm.processes[id]
	pm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("process %q not found", id)
	}

	proc.mu.Lock()
	running := proc.running
	proc.mu.Unlock()

	if !running {
		return nil
	}

	// SIGTERM first
	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Already dead
		return nil
	}

	// Wait up to 5s, then SIGKILL
	select {
	case <-proc.exitCh:
		return nil
	case <-time.After(5 * time.Second):
		proc.cmd.Process.Kill()
		<-proc.exitCh
		return nil
	}
}

func (pm *ProcessManager) Remove(id string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if proc, ok := pm.processes[id]; ok {
		// Close all subscribers
		proc.subsMu.Lock()
		for sub := range proc.subs {
			close(sub.ch)
			delete(proc.subs, sub)
		}
		proc.subsMu.Unlock()
		delete(pm.processes, id)
	}
}

func (pm *ProcessManager) Get(id string) (*Process, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.processes[id]
	return p, ok
}

func (pm *ProcessManager) List() []ProcessInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var result []ProcessInfo
	for _, p := range pm.processes {
		p.mu.Lock()
		result = append(result, ProcessInfo{
			ID:       p.id,
			PID:      p.pid,
			Running:  p.running,
			ExitCode: p.exitCode,
		})
		p.mu.Unlock()
	}
	return result
}

func (pm *ProcessManager) Output(id string) ([]byte, error) {
	pm.mu.RLock()
	proc, ok := pm.processes[id]
	pm.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("process %q not found", id)
	}
	proc.mu.Lock()
	data := proc.ring.Bytes()
	proc.mu.Unlock()
	return data, nil
}

func (p *Process) Subscribe() *subscriber {
	sub := &subscriber{ch: make(chan Event, 256)}
	p.subsMu.Lock()
	p.subs[sub] = struct{}{}
	p.subsMu.Unlock()
	return sub
}

func (p *Process) Unsubscribe(sub *subscriber) {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	if _, ok := p.subs[sub]; ok {
		delete(p.subs, sub)
		// Don't close channel here — let the reader drain
	}
}

func (p *Process) SendInput(data []byte) error {
	_, err := p.ptmx.Write(data)
	return err
}

func (p *Process) Resize(cols, rows int) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

func (pm *ProcessManager) Count() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.processes)
}

func (pm *ProcessManager) RunningCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, p := range pm.processes {
		p.mu.Lock()
		if p.running {
			count++
		}
		p.mu.Unlock()
	}
	return count
}

// ShutdownAll sends SIGHUP to all processes, waits 5s, then SIGKILL.
func (pm *ProcessManager) ShutdownAll() {
	pm.mu.RLock()
	var procs []*Process
	for _, p := range pm.processes {
		procs = append(procs, p)
	}
	pm.mu.RUnlock()

	// Send SIGHUP to all
	for _, p := range procs {
		p.mu.Lock()
		running := p.running
		p.mu.Unlock()
		if running {
			p.cmd.Process.Signal(syscall.SIGHUP)
		}
	}

	// Wait up to 5s for all to exit
	deadline := time.After(5 * time.Second)
	for _, p := range procs {
		select {
		case <-p.exitCh:
		case <-deadline:
			// Force kill remaining
			p.mu.Lock()
			running := p.running
			p.mu.Unlock()
			if running {
				p.cmd.Process.Kill()
			}
		}
	}
}

// wrapCommand wraps a command for SSH or Docker execution.
func wrapCommand(command, host string) string {
	if host == "" || host == "local" {
		return command
	}
	if strings.HasPrefix(host, "ssh:") {
		hostname := strings.TrimPrefix(host, "ssh:")
		return fmt.Sprintf("ssh -t %s %s", hostname, shellescape(command))
	}
	if strings.HasPrefix(host, "docker:") {
		container := strings.TrimPrefix(host, "docker:")
		return fmt.Sprintf("docker exec -it %s sh -c %s", container, shellescape(command))
	}
	return command
}

// cleanEnv returns a sanitized environment for child processes,
// removing vars that cause Claude Code to think it's nested.
func cleanEnv(env []string) []string {
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
	out = append(out, "TERM=xterm-256color")
	return out
}

func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
