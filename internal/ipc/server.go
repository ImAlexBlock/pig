package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Handler serves one request from a connection.
type Handler func(ctx context.Context, c *Conn, method string, params json.RawMessage) (any, error)

// Conn is one accepted client connection.
type Conn struct {
	id     int
	conn   net.Conn
	wmu    sync.Mutex
	closed bool
	// State is free-form per-connection storage for the daemon.
	State map[string]any
}

// ID returns the connection identifier.
func (c *Conn) ID() int { return c.id }

// RemoteUID reports the peer uid when the platform exposes it (-1 otherwise).
func (c *Conn) RemoteUID() int { return remoteUID(c.conn) }

// Send pushes a notification to the client.
func (c *Conn) Send(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.SendRaw(method, raw)
}

// SendRaw pushes a pre-encoded notification.
func (c *Conn) SendRaw(method string, params json.RawMessage) error {
	msg := Notification{JSONRPC: "2.0", Method: method, Params: params}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return errors.New("ipc: connection closed")
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		c.closed = true
		return err
	}
	return nil
}

func (c *Conn) close() {
	c.wmu.Lock()
	c.closed = true
	c.wmu.Unlock()
	_ = c.conn.Close()
}

// Server accepts connections and dispatches requests.
type Server struct {
	Handler Handler
	// OnConnect and OnClose let the daemon track subscribers.
	OnConnect func(c *Conn)
	OnClose   func(c *Conn)

	mu     sync.Mutex
	conns  map[*Conn]struct{}
	nextID int
	ln     net.Listener
}

// NewServer wraps a listener.
func NewServer(ln net.Listener) *Server {
	return &Server{conns: map[*Conn]struct{}{}, ln: ln}
}

// Serve accepts connections until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.close()
		}
		s.mu.Unlock()
	}()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		s.nextID++
		c := &Conn{id: s.nextID, conn: nc, State: map[string]any{}}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		if s.OnConnect != nil {
			s.OnConnect(c)
		}
		go s.serveConn(ctx, c)
	}
}

// ConnCount reports the number of live connections.
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) serveConn(ctx context.Context, c *Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		if s.OnClose != nil {
			s.OnClose(c)
		}
		c.close()
	}()
	r := bufio.NewReaderSize(c.conn, 1<<20)
	for {
		line, err := readMessage(r)
		if err != nil {
			return
		}
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = c.SendRaw("ipc.error", mustJSON(map[string]any{"code": CodeParseError, "message": err.Error()}))
			continue
		}
		go s.dispatch(ctx, c, req)
	}
}

func (s *Server) dispatch(ctx context.Context, c *Conn, req Request) {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if s.Handler == nil {
		resp.Error = &Error{Code: CodeMethodNotFound, Message: "no handler"}
	} else {
		result, err := s.Handler(ctx, c, req.Method, req.Params)
		if err != nil {
			var rpcErr *Error
			if errors.As(err, &rpcErr) {
				resp.Error = rpcErr
			} else if errors.Is(err, ErrMethodNotFound) {
				resp.Error = &Error{Code: CodeMethodNotFound, Message: req.Method + ": method not found"}
			} else {
				resp.Error = &Error{Code: CodeApp, Message: err.Error()}
			}
		} else {
			raw, merr := json.Marshal(result)
			if merr != nil {
				resp.Error = &Error{Code: CodeInternalError, Message: merr.Error()}
			} else {
				resp.Result = raw
			}
		}
	}
	if req.ID == nil {
		return
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, _ = c.conn.Write(append(b, '\n'))
}

// ErrMethodNotFound reports an unknown method.
var ErrMethodNotFound = errors.New("method not found")

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
