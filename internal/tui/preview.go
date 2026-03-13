package tui

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/arlintdev/claudes/internal/daemon"
	"github.com/taigrr/bubbleterm"
	tea "charm.land/bubbletea/v2"
)

// PreviewPane wraps a bubbleterm.Model to show live terminal output from a daemon process.
type PreviewPane struct {
	mu         sync.Mutex
	term       *bubbleterm.Model
	instanceID string
	client     *daemon.Client
	pipeW      io.WriteCloser // write end for feeding data to bubbleterm
	stopFwd    chan struct{}   // signals forwardDaemonOutput to exit
	focused    bool
	width      int
	height     int
}

// NewPreviewPane creates a preview pane (initially detached).
func NewPreviewPane(client *daemon.Client) *PreviewPane {
	return &PreviewPane{
		client: client,
	}
}

// Attach connects the preview to a daemon process, loading scrollback and subscribing.
func (p *PreviewPane) Attach(instanceID string, width, height int) tea.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Detach from previous
	p.detachLocked()

	p.instanceID = instanceID
	p.width = width
	p.height = height

	// Create pipe: we write daemon output → pipe → bubbleterm reads
	pr, pw := io.Pipe()
	p.pipeW = pw

	// Create stop channel for the forwarder goroutine
	stop := make(chan struct{})
	p.stopFwd = stop

	// Create bubbleterm with pipe (read from pr, write goes nowhere — input handled via daemon)
	term, err := bubbleterm.NewWithPipes(width, height, pr, &nopWriteCloser{})
	if err != nil {
		pw.Close()
		pr.Close()
		return nil
	}
	p.term = term

	id := instanceID
	client := p.client
	writer := pw

	w, h := width, height
	return func() tea.Msg {
		// Subscribe first so we don't miss any output
		_ = client.Subscribe(id)
		go forwardDaemonOutput(client, id, writer, stop)

		// Resize PTY to match preview — sends SIGWINCH triggering redraw
		_ = client.Resize(id, w, h)

		// Brief pause to let the redraw propagate into the ring buffer
		time.Sleep(50 * time.Millisecond)

		// Load scrollback (now contains post-resize content)
		data, err := client.Output(id)
		if err == nil && len(data) > 0 {
			writer.Write(data)
		}

		return previewAttachedMsg{id: id}
	}
}

// forwardDaemonOutput reads events from the daemon client and writes output to the pipe.
// Exits when stop is closed.
func forwardDaemonOutput(client *daemon.Client, id string, w io.Writer, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case evt, ok := <-client.Events():
			if !ok {
				return
			}
			if evt.Event != daemon.EventOutput {
				continue
			}
			var od daemon.OutputEventData
			if err := json.Unmarshal(evt.Data, &od); err != nil {
				continue
			}
			if od.ID != id {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(od.Data)
			if err != nil {
				continue
			}
			w.Write(raw)
		}
	}
}

type previewAttachedMsg struct {
	id string
}

// Detach disconnects from the current process.
func (p *PreviewPane) Detach() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.detachLocked()
}

func (p *PreviewPane) detachLocked() {
	if p.instanceID == "" {
		return
	}
	// Stop the forwarder goroutine first
	if p.stopFwd != nil {
		close(p.stopFwd)
		p.stopFwd = nil
	}
	if p.client != nil && p.instanceID != "" {
		_ = p.client.Unsubscribe(p.instanceID)
	}
	if p.term != nil {
		p.term.Close()
		p.term = nil
	}
	if p.pipeW != nil {
		p.pipeW.Close()
		p.pipeW = nil
	}
	p.instanceID = ""
}

// InstanceID returns the currently attached instance.
func (p *PreviewPane) InstanceID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instanceID
}

// IsAttached returns true if the preview is connected to a process.
func (p *PreviewPane) IsAttached() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instanceID != ""
}

// Focus enables keyboard input forwarding.
func (p *PreviewPane) Focus() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.focused = true
	if p.term != nil {
		p.term.Focus()
	}
}

// Blur disables keyboard input forwarding.
func (p *PreviewPane) Blur() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.focused = false
	if p.term != nil {
		p.term.Blur()
	}
}

// IsFocused returns whether the preview is focused.
func (p *PreviewPane) IsFocused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.focused
}

// Init returns the bubbleterm Init command.
func (p *PreviewPane) Init() tea.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.term != nil {
		return p.term.Init()
	}
	return nil
}

// Update forwards messages to bubbleterm.
func (p *PreviewPane) Update(msg tea.Msg) tea.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.term == nil {
		return nil
	}
	newModel, cmd := p.term.Update(msg)
	if t, ok := newModel.(*bubbleterm.Model); ok {
		p.term = t
	}
	return cmd
}

// View returns the terminal view.
func (p *PreviewPane) View() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.term == nil {
		return ""
	}
	v := p.term.View()
	return v.Content
}

// Resize updates the preview dimensions and resizes the daemon PTY.
func (p *PreviewPane) Resize(width, height int) tea.Cmd {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.width = width
	p.height = height
	// Resize the daemon PTY to match
	if p.client != nil && p.instanceID != "" {
		go p.client.Resize(p.instanceID, width, height)
	}
	if p.term != nil {
		return p.term.Resize(width, height)
	}
	return nil
}

// SendInput sends raw bytes to the daemon process.
func (p *PreviewPane) SendInput(data []byte) {
	p.mu.Lock()
	id := p.instanceID
	client := p.client
	p.mu.Unlock()
	if id != "" && client != nil {
		_ = client.SendInput(id, data)
	}
}

// nopWriteCloser is a no-op WriteCloser for bubbleterm's write pipe.
type nopWriteCloser struct{}

func (n *nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (n *nopWriteCloser) Close() error                { return nil }
