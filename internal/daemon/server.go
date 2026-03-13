package daemon

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// Server listens on a Unix socket and manages client connections.
type Server struct {
	pm       *ProcessManager
	listener net.Listener

	clientsMu sync.Mutex
	clients   map[*clientConn]struct{}
	clientCnt atomic.Int64 // for idle tracking
}

type clientConn struct {
	conn   net.Conn
	enc    *json.Encoder
	encMu  sync.Mutex
	server *Server

	// Per-connection subscriptions: processID → subscriber
	subsMu sync.Mutex
	subs   map[string]*subscriber
}

func NewServer(pm *ProcessManager, listener net.Listener) *Server {
	return &Server{
		pm:       pm,
		listener: listener,
		clients:  make(map[*clientConn]struct{}),
	}
}

func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return err
		}
		cc := &clientConn{
			conn:   conn,
			enc:    json.NewEncoder(conn),
			server: s,
			subs:   make(map[string]*subscriber),
		}
		s.clientsMu.Lock()
		s.clients[cc] = struct{}{}
		s.clientsMu.Unlock()
		s.clientCnt.Add(1)
		go cc.serve()
	}
}

func (s *Server) ClientCount() int {
	return int(s.clientCnt.Load())
}

func (s *Server) Close() error {
	return s.listener.Close()
}

func (cc *clientConn) serve() {
	defer cc.cleanup()
	scanner := bufio.NewScanner(cc.conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			cc.sendError("", fmt.Sprintf("invalid json: %v", err))
			continue
		}
		cc.handleRequest(req)
	}
}

func (cc *clientConn) cleanup() {
	// Unsubscribe all
	cc.subsMu.Lock()
	for procID, sub := range cc.subs {
		if proc, ok := cc.server.pm.Get(procID); ok {
			proc.Unsubscribe(sub)
		}
		delete(cc.subs, procID)
	}
	cc.subsMu.Unlock()

	cc.conn.Close()
	cc.server.clientsMu.Lock()
	delete(cc.server.clients, cc)
	cc.server.clientsMu.Unlock()
	cc.server.clientCnt.Add(-1)
}

func (cc *clientConn) handleRequest(req Request) {
	switch req.Method {
	case MethodPing:
		cc.sendResult(req.ID, PingResult{Pong: true})

	case MethodLaunch:
		var p LaunchParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		proc, err := cc.server.pm.Launch(p)
		if err != nil {
			cc.sendError(req.ID, err.Error())
			return
		}
		cc.sendResult(req.ID, LaunchResult{PID: proc.pid})

	case MethodKill:
		var p KillParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		if err := cc.server.pm.Kill(p.ID); err != nil {
			cc.sendError(req.ID, err.Error())
			return
		}
		cc.sendResult(req.ID, struct{}{})

	case MethodList:
		cc.sendResult(req.ID, ListResult{Processes: cc.server.pm.List()})

	case MethodOutput:
		var p OutputParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		data, err := cc.server.pm.Output(p.ID)
		if err != nil {
			cc.sendError(req.ID, err.Error())
			return
		}
		cc.sendResult(req.ID, OutputResult{Data: base64.StdEncoding.EncodeToString(data)})

	case MethodSubscribe:
		var p SubscribeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		proc, ok := cc.server.pm.Get(p.ID)
		if !ok {
			cc.sendError(req.ID, fmt.Sprintf("process %q not found", p.ID))
			return
		}
		cc.subsMu.Lock()
		if oldSub, exists := cc.subs[p.ID]; exists {
			proc.Unsubscribe(oldSub)
		}
		sub := proc.Subscribe()
		cc.subs[p.ID] = sub
		cc.subsMu.Unlock()

		// Start forwarding events to this client
		go cc.forwardEvents(p.ID, sub)
		cc.sendResult(req.ID, struct{}{})

	case MethodUnsubscribe:
		var p UnsubscribeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		cc.subsMu.Lock()
		if sub, exists := cc.subs[p.ID]; exists {
			if proc, ok := cc.server.pm.Get(p.ID); ok {
				proc.Unsubscribe(sub)
			}
			delete(cc.subs, p.ID)
		}
		cc.subsMu.Unlock()
		cc.sendResult(req.ID, struct{}{})

	case MethodInput:
		var p InputParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		proc, ok := cc.server.pm.Get(p.ID)
		if !ok {
			cc.sendError(req.ID, fmt.Sprintf("process %q not found", p.ID))
			return
		}
		data, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid base64: %v", err))
			return
		}
		if err := proc.SendInput(data); err != nil {
			cc.sendError(req.ID, err.Error())
			return
		}
		cc.sendResult(req.ID, struct{}{})

	case MethodResize:
		var p ResizeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			cc.sendError(req.ID, fmt.Sprintf("invalid params: %v", err))
			return
		}
		proc, ok := cc.server.pm.Get(p.ID)
		if !ok {
			cc.sendError(req.ID, fmt.Sprintf("process %q not found", p.ID))
			return
		}
		if err := proc.Resize(p.Cols, p.Rows); err != nil {
			cc.sendError(req.ID, err.Error())
			return
		}
		cc.sendResult(req.ID, struct{}{})

	default:
		cc.sendError(req.ID, fmt.Sprintf("unknown method: %q", req.Method))
	}
}

func (cc *clientConn) forwardEvents(procID string, sub *subscriber) {
	for evt := range sub.ch {
		cc.encMu.Lock()
		if err := cc.enc.Encode(evt); err != nil {
			cc.encMu.Unlock()
			return
		}
		cc.encMu.Unlock()
	}
}

func (cc *clientConn) sendResult(id string, result any) {
	data, err := json.Marshal(result)
	if err != nil {
		log.Printf("marshal result: %v", err)
		return
	}
	resp := Response{ID: id, Result: data}
	cc.encMu.Lock()
	defer cc.encMu.Unlock()
	cc.enc.Encode(resp)
}

func (cc *clientConn) sendError(id, msg string) {
	resp := Response{ID: id, Error: msg}
	cc.encMu.Lock()
	defer cc.encMu.Unlock()
	cc.enc.Encode(resp)
}
