package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client is a JSON-RPC client over a Unix socket.
type Client struct {
	conn net.Conn
	r    *bufio.Reader

	wmu sync.Mutex

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan *Response
	handlers map[string]func(json.RawMessage)

	doneOnce sync.Once
	done     chan struct{}
	errMu    sync.Mutex
	err      error
	closed   bool
}

// Dial connects to path, retrying briefly while the daemon starts.
func Dial(ctx context.Context, path string) (*Client, error) {
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 2*time.Second)
		if err == nil {
			return newClient(conn), nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("ipc: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("ipc: dial %s: %w", path, lastErr)
}

func newClient(conn net.Conn) *Client {
	c := &Client{
		conn:     conn,
		r:        bufio.NewReaderSize(conn, 1<<20),
		pending:  map[int64]chan *Response{},
		handlers: map[string]func(json.RawMessage){},
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	defer func() {
		c.errMu.Lock()
		if c.err == nil {
			c.err = errors.New("ipc: connection closed")
		}
		c.closed = true
		c.errMu.Unlock()
		close(c.done)
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}()
	for {
		line, err := readMessage(c.r)
		if err != nil {
			return
		}
		if len(line) == 0 {
			continue
		}
		var probe struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *Error          `json:"error"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if probe.ID != nil && probe.Method == "" {
			c.mu.Lock()
			ch, ok := c.pending[*probe.ID]
			delete(c.pending, *probe.ID)
			c.mu.Unlock()
			if ok {
				ch <- &Response{ID: probe.ID, Result: probe.Result, Error: probe.Error}
			}
			continue
		}
		if probe.Method != "" {
			c.mu.Lock()
			h := c.handlers[probe.Method]
			c.mu.Unlock()
			if h != nil {
				var params json.RawMessage
				var env struct {
					Params json.RawMessage `json:"params"`
				}
				if err := json.Unmarshal(line, &env); err == nil {
					params = env.Params
				}
				h(params)
			}
		}
	}
}

// On registers a notification handler.
func (c *Client) On(method string, h func(json.RawMessage)) {
	c.mu.Lock()
	c.handlers[method] = h
	c.mu.Unlock()
}

// Done is closed when the connection drops.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err reports why the connection closed.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Call performs one request.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := Request{JSONRPC: "2.0", ID: &id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("ipc: encode params: %v", err)
		}
		req.Params = raw
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ipc: encode: %v", err)
	}
	c.wmu.Lock()
	_, werr := c.conn.Write(append(b, '\n'))
	c.wmu.Unlock()
	if werr != nil {
		return nil, fmt.Errorf("ipc: write: %v", werr)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("ipc: connection closed")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("ipc: %v", ctx.Err())
	case <-c.done:
		return nil, fmt.Errorf("ipc: connection closed")
	}
}

// Decode unmarshals a raw result into out.
func Decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
