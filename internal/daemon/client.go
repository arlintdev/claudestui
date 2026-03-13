package daemon

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Client connects to the daemon over a Unix socket.
type Client struct {
	conn   net.Conn
	enc    *json.Encoder
	encMu  sync.Mutex

	// Pending request callbacks
	pending   map[string]chan Response
	pendingMu sync.Mutex

	// Event channel for server-push events
	events chan Event
	nextID atomic.Uint64

	done chan struct{}
}

// Dial connects to the daemon socket.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:    conn,
		enc:     json.NewEncoder(conn),
		pending: make(map[string]chan Response),
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Try to determine if this is a Response or Event
		// Responses have "id" field, Events have "event" field
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		if _, hasEvent := raw["event"]; hasEvent {
			var evt Event
			json.Unmarshal(line, &evt)
			select {
			case c.events <- evt:
			default:
			}
		} else if idRaw, hasID := raw["id"]; hasID {
			var id string
			json.Unmarshal(idRaw, &id)
			var resp Response
			json.Unmarshal(line, &resp)
			c.pendingMu.Lock()
			if ch, ok := c.pending[id]; ok {
				ch <- resp
				delete(c.pending, id)
			}
			c.pendingMu.Unlock()
		}
	}

	// Connection closed — unblock any pending requests
	c.pendingMu.Lock()
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = make(map[string]chan Response)
	c.pendingMu.Unlock()
}

func (c *Client) call(method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	ch := make(chan Response, 1)

	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	var paramsRaw json.RawMessage
	if params != nil {
		var err error
		paramsRaw, err = json.Marshal(params)
		if err != nil {
			return nil, err
		}
	}

	req := Request{ID: id, Method: method, Params: paramsRaw}
	c.encMu.Lock()
	err := c.enc.Encode(req)
	c.encMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("send: %w", err)
	}

	resp, ok := <-ch
	if !ok {
		return nil, fmt.Errorf("connection closed")
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Result, nil
}

// Ping checks if the daemon is alive.
func (c *Client) Ping() (bool, error) {
	result, err := c.call(MethodPing, nil)
	if err != nil {
		return false, err
	}
	var r PingResult
	json.Unmarshal(result, &r)
	return r.Pong, nil
}

// Launch starts a new process with a PTY.
func (c *Client) Launch(params LaunchParams) (int, error) {
	result, err := c.call(MethodLaunch, params)
	if err != nil {
		return 0, err
	}
	var r LaunchResult
	json.Unmarshal(result, &r)
	return r.PID, nil
}

// Kill terminates a process.
func (c *Client) Kill(id string) error {
	_, err := c.call(MethodKill, KillParams{ID: id})
	return err
}

// List returns all managed processes.
func (c *Client) List() ([]ProcessInfo, error) {
	result, err := c.call(MethodList, nil)
	if err != nil {
		return nil, err
	}
	var r ListResult
	json.Unmarshal(result, &r)
	if r.Processes == nil {
		r.Processes = []ProcessInfo{}
	}
	return r.Processes, nil
}

// Output returns the scrollback buffer for a process.
func (c *Client) Output(id string) ([]byte, error) {
	result, err := c.call(MethodOutput, OutputParams{ID: id})
	if err != nil {
		return nil, err
	}
	var r OutputResult
	json.Unmarshal(result, &r)
	return base64.StdEncoding.DecodeString(r.Data)
}

// Subscribe starts streaming output events for a process.
func (c *Client) Subscribe(id string) error {
	_, err := c.call(MethodSubscribe, SubscribeParams{ID: id})
	return err
}

// Unsubscribe stops streaming output events for a process.
func (c *Client) Unsubscribe(id string) error {
	_, err := c.call(MethodUnsubscribe, UnsubscribeParams{ID: id})
	return err
}

// SendInput sends keystrokes to a process PTY.
func (c *Client) SendInput(id string, data []byte) error {
	_, err := c.call(MethodInput, InputParams{
		ID:   id,
		Data: base64.StdEncoding.EncodeToString(data),
	})
	return err
}

// Resize changes the PTY dimensions.
func (c *Client) Resize(id string, cols, rows int) error {
	_, err := c.call(MethodResize, ResizeParams{ID: id, Cols: cols, Rows: rows})
	return err
}

// Events returns a channel of server-push events (output, exit).
func (c *Client) Events() <-chan Event {
	return c.events
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Done returns a channel that's closed when the connection drops.
func (c *Client) Done() <-chan struct{} {
	return c.done
}
