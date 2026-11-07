package ipc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func tempSocket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.sock")
}

func startServer(t *testing.T, handler Handler) (*Server, string) {
	t.Helper()
	sock := tempSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer(ln)
	srv.Handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()
	return srv, sock
}

func dial(t *testing.T, sock string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRoundTrip(t *testing.T) {
	_, sock := startServer(t, func(ctx context.Context, c *Conn, method string, params json.RawMessage) (any, error) {
		switch method {
		case "echo":
			var v map[string]any
			if err := json.Unmarshal(params, &v); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
			}
			return v, nil
		}
		return nil, ErrMethodNotFound
	})
	c := dial(t, sock)
	ctx := context.Background()
	raw, err := c.Call(ctx, "echo", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["hello"] != "world" {
		t.Fatalf("result = %v", got)
	}

	_, err = c.Call(ctx, "nope", nil)
	if err == nil {
		t.Fatal("unknown method succeeded")
	}
	var rpcErr *Error
	if !asError(err, &rpcErr) {
		t.Fatalf("error type = %T", err)
	}
	if rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", rpcErr.Code, CodeMethodNotFound)
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func TestEventBeforeResponse(t *testing.T) {
	_, sock := startServer(t, func(ctx context.Context, c *Conn, method string, params json.RawMessage) (any, error) {
		if method == "notify" {
			if err := c.Send("test.event", map[string]any{"n": float64(1)}); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		}
		return nil, ErrMethodNotFound
	})
	c := dial(t, sock)

	var mu sync.Mutex
	var order []string
	c.On("test.event", func(raw json.RawMessage) {
		mu.Lock()
		order = append(order, "event")
		mu.Unlock()
	})
	if _, err := c.Call(context.Background(), "notify", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 1 || order[0] != "event" {
		t.Fatalf("notification order = %v, want one event before the response", order)
	}
}

func TestFixturesParse(t *testing.T) {
	for _, name := range []string{"request.json", "response.json", "error.json", "notification.json"} {
		data, err := os.ReadFile(filepath.Join("../../testdata/ipc", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		switch name {
		case "request.json":
			var req Request
			if err := json.Unmarshal(data, &req); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if req.Method != "daemon.status" || req.ID == nil {
				t.Fatalf("%s parsed as %+v", name, req)
			}
		case "error.json":
			var resp Response
			if err := json.Unmarshal(data, &resp); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if resp.Error == nil || resp.Error.Code != CodeMethodNotFound {
				t.Fatalf("%s parsed as %+v", name, resp)
			}
		case "notification.json":
			var n Notification
			if err := json.Unmarshal(data, &n); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if n.Method != "run.event" {
				t.Fatalf("%s parsed as %+v", name, n)
			}
		}
	}
}
