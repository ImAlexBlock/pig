// Package ipc implements newline-delimited JSON-RPC 2.0 over a Unix socket.
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	// CodeApp is used for application-level failures.
	CodeApp = -32000
)

// Request is a JSON-RPC request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Notification is a server-initiated message.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// AppError builds an application error.
func AppError(format string, args ...any) *Error {
	return &Error{Code: CodeApp, Message: fmt.Sprintf(format, args...)}
}

// readMessage reads one newline-delimited JSON object.
func readMessage(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		if len(line) > 0 && err == io.EOF {
			return line, nil
		}
		return nil, err
	}
	return line, nil
}
