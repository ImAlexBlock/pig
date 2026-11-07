// Package provider implements the model wire adapters, failover chains and
// usage accounting.
package provider

import (
	"context"
	"encoding/json"
)

// EventType enumerates streamed provider events.
type EventType int

const (
	EvTextStart EventType = iota
	EvTextDelta
	EvTextEnd
	EvThinkingStart
	EvThinkingDelta
	EvThinkingEnd
	EvToolStart
	EvToolDelta
	EvToolEnd
	EvImage
	EvDone
	EvError
)

func (t EventType) String() string {
	switch t {
	case EvTextStart:
		return "text_start"
	case EvTextDelta:
		return "text_delta"
	case EvTextEnd:
		return "text_end"
	case EvThinkingStart:
		return "thinking_start"
	case EvThinkingDelta:
		return "thinking_delta"
	case EvThinkingEnd:
		return "thinking_end"
	case EvToolStart:
		return "tool_start"
	case EvToolDelta:
		return "tool_delta"
	case EvToolEnd:
		return "tool_end"
	case EvImage:
		return "image"
	case EvDone:
		return "done"
	case EvError:
		return "error"
	}
	return "unknown"
}

// Event is one streamed delta.
type Event struct {
	Type     EventType `json:"type"`
	Index    int       `json:"index,omitempty"`
	Text     string    `json:"text,omitempty"`
	ToolID   string    `json:"tool_id,omitempty"`
	ToolName string    `json:"tool_name,omitempty"`
	// Reset asks the consumer to discard text accumulated so far for the
	// current assistant turn (set when a partially streamed attempt failed and
	// failover restarted the request).
	Reset bool `json:"reset,omitempty"`
}

// Usage reports token accounting for one request.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Image is inline image data.
type Image struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"-"`
}

// Block types.
const (
	BlockText       = "text"
	BlockThinking   = "thinking"
	BlockImage      = "image"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Block is one content block inside a message.
type Block struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Image    *Image          `json:"image,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
}

// Message is one conversation turn in wire-neutral form.
type Message struct {
	Role   string          `json:"role"`
	Blocks []Block         `json:"blocks"`
	Raw    json.RawMessage `json:"raw,omitempty"`
}

// ToolSpec is one tool definition offered to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Request is one provider call.
type Request struct {
	Model      string
	System     []Block
	Messages   []Message
	Tools      []ToolSpec
	MaxTokens  int
	SessionID  string
	ImageInput bool
	// Sampling, forwarded when set.
	Temperature *float64
	TopP        *float64
	Seed        *int64
	// MaxTokensField overrides the token-limit key for openai-chat
	// (e.g. "max_completion_tokens").
	MaxTokensField string
}

// StopReason is the normalised completion reason.
type StopReason int

const (
	StopEnd StopReason = iota
	StopLength
	StopToolUse
	StopError
)

func (s StopReason) String() string {
	switch s {
	case StopEnd:
		return "end"
	case StopLength:
		return "length"
	case StopToolUse:
		return "tool_use"
	case StopError:
		return "error"
	}
	return "unknown"
}

// Result is one completed provider call.
type Result struct {
	Message Message
	Usage   Usage
	Stop    StopReason
	Raw     json.RawMessage
}

// Client is a wire adapter.
type Client interface {
	Kind() string
	Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error)
}

// rawEnvelope is the stored form of provider-native message payloads.
type rawEnvelope struct {
	Wire    string          `json:"wire"`
	Payload json.RawMessage `json:"payload"`
}

// RawEnvelope wraps a provider-native payload for replay on the same wire.
func RawEnvelope(wire string, payload any) json.RawMessage {
	var p json.RawMessage
	switch v := payload.(type) {
	case json.RawMessage:
		p = v
	case []byte:
		p = json.RawMessage(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		p = b
	}
	b, err := json.Marshal(rawEnvelope{Wire: wire, Payload: p})
	if err != nil {
		return nil
	}
	return b
}

// RawFor returns the payload when raw belongs to the given wire.
func RawFor(raw json.RawMessage, wire string) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var env rawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if env.Wire != wire || len(env.Payload) == 0 {
		return nil, false
	}
	return env.Payload, true
}

// TextOf concatenates the text blocks of a message.
func TextOf(m Message) string {
	out := ""
	for _, b := range m.Blocks {
		if b.Type == BlockText {
			out += b.Text
		}
	}
	return out
}

// ToolCalls returns the tool_use blocks of a message.
func ToolCalls(m Message) []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}
