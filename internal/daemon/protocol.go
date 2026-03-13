package daemon

import "encoding/json"

// Methods
const (
	MethodLaunch      = "launch"
	MethodKill        = "kill"
	MethodList        = "list"
	MethodOutput      = "output"
	MethodSubscribe   = "subscribe"
	MethodUnsubscribe = "unsubscribe"
	MethodInput       = "input"
	MethodResize      = "resize"
	MethodPing        = "ping"
)

// Event types (server → client push)
const (
	EventOutput = "output"
	EventExit   = "exit"
)

// Request is a JSON-line message from client to server.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-line message from server to client.
type Response struct {
	ID     string          `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Event is a server-push message (no ID).
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// --- Param types ---

type LaunchParams struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

type LaunchResult struct {
	PID int `json:"pid"`
}

type KillParams struct {
	ID string `json:"id"`
}

type ListResult struct {
	Processes []ProcessInfo `json:"processes"`
}

type ProcessInfo struct {
	ID       string `json:"id"`
	PID      int    `json:"pid"`
	Running  bool   `json:"running"`
	ExitCode int    `json:"exit_code"`
}

type OutputParams struct {
	ID    string `json:"id"`
	Lines int    `json:"lines"` // 0 = all buffered
}

type OutputResult struct {
	Data string `json:"data"` // base64
}

type SubscribeParams struct {
	ID string `json:"id"`
}

type UnsubscribeParams struct {
	ID string `json:"id"`
}

type InputParams struct {
	ID   string `json:"id"`
	Data string `json:"data"` // base64
}

type ResizeParams struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type PingResult struct {
	Pong bool `json:"pong"`
}

// --- Event data types ---

type OutputEventData struct {
	ID   string `json:"id"`
	Data string `json:"data"` // base64
}

type ExitEventData struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
}
