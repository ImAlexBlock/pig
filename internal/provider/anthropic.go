package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type anthropicClient struct {
	name string
	base string
	key  string
	http *http.Client
}

func (c *anthropicClient) Kind() string { return "anthropic" }

type anthropicSystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model       string                 `json:"model"`
	MaxTokens   int                    `json:"max_tokens"`
	System      []anthropicSystemBlock `json:"system,omitempty"`
	Messages    []anthropicMessage     `json:"messages"`
	Tools       []anthropicTool        `json:"tools,omitempty"`
	Stream      bool                   `json:"stream"`
	Temperature *float64               `json:"temperature,omitempty"`
	TopP        *float64               `json:"top_p,omitempty"`
}

func (c *anthropicClient) buildBody(req Request) ([]byte, error) {
	out := anthropicRequest{Model: req.Model, MaxTokens: req.MaxTokens, Stream: true,
		Temperature: req.Temperature, TopP: req.TopP}
	for _, b := range req.System {
		if b.Type != BlockText || b.Text == "" {
			continue
		}
		out.System = append(out.System, anthropicSystemBlock{Type: "text", Text: b.Text})
	}
	if n := len(out.System); n > 0 {
		out.System[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	if n := len(out.Tools); n > 0 {
		out.Tools[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	for _, m := range MergeMessages(req.Messages) {
		content, err := c.encodeContent(m)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, anthropicMessage{Role: m.Role, Content: content})
	}
	return json.Marshal(out)
}

func (c *anthropicClient) encodeContent(m Message) (json.RawMessage, error) {
	if m.Role == "assistant" {
		if payload, ok := RawFor(m.Raw, "anthropic"); ok {
			var obj struct {
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(payload, &obj); err == nil && len(obj.Content) > 0 {
				return obj.Content, nil
			}
		}
	}
	var blocks []any
	for _, b := range m.Blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
			blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
		case BlockImage:
			if b.Image == nil {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": b.Image.MediaType,
					"data":       base64.StdEncoding.EncodeToString(b.Image.Data),
				},
			})
		case BlockToolUse:
			var input any = map[string]any{}
			if len(b.Args) > 0 {
				if err := json.Unmarshal(b.Args, &input); err != nil {
					input = map[string]any{}
				}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": b.ToolID, "name": b.ToolName, "input": input})
		case BlockToolResult:
			blocks = append(blocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": b.ToolID,
				"content":     b.Text,
				"is_error":    b.IsError,
			})
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}
	return json.Marshal(blocks)
}

// MergeMessages joins adjacent messages that share a role.
func MergeMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role && len(m.Raw) == 0 {
			out[n-1].Blocks = append(out[n-1].Blocks, m.Blocks...)
			continue
		}
		out = append(out, m)
	}
	return out
}

type contentAcc struct {
	kind      string
	text      strings.Builder
	toolID    string
	toolName  string
	args      strings.Builder
	signature string
}

func (c *anthropicClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error) {
	payload, err := c.buildBody(req)
	if err != nil {
		return Result{}, &ProviderError{Class: ClassInvalidRequest, Message: err.Error()}
	}
	rctx, cancel := context.WithTimeout(ctx, WholeRequestTimeout)
	defer cancel()
	body, pe := postStream(rctx, c.http, c.base+"/v1/messages", map[string]string{
		"x-api-key":         c.key,
		"anthropic-version": "2023-06-01",
	}, payload)
	if pe != nil {
		return Result{}, pe
	}

	var usage Usage
	var stop string
	accs := map[int]*contentAcc{}
	var order []int

	handle := func(event, data string) error {
		switch event {
		case "ping":
			return nil
		case "message_start":
			var msg struct {
				Message struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &msg); err == nil {
				usage.Input = msg.Message.Usage.InputTokens
				usage.CacheWrite = msg.Message.Usage.CacheCreationInputTokens
				usage.CacheRead = msg.Message.Usage.CacheReadInputTokens
			}
			return nil
		case "content_block_start":
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type     string `json:"type"`
					ID       string `json:"id"`
					Name     string `json:"name"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return nil
			}
			acc := &contentAcc{kind: ev.ContentBlock.Type, toolID: ev.ContentBlock.ID, toolName: ev.ContentBlock.Name}
			accs[ev.Index] = acc
			order = append(order, ev.Index)
			switch ev.ContentBlock.Type {
			case "text":
				if ev.ContentBlock.Text != "" {
					acc.text.WriteString(ev.ContentBlock.Text)
				}
				return emit(Event{Type: EvTextStart, Index: ev.Index})
			case "thinking":
				if ev.ContentBlock.Thinking != "" {
					acc.text.WriteString(ev.ContentBlock.Thinking)
				}
				return emit(Event{Type: EvThinkingStart, Index: ev.Index})
			case "tool_use":
				return emit(Event{Type: EvToolStart, Index: ev.Index, ToolID: acc.toolID, ToolName: acc.toolName})
			}
			return nil
		case "content_block_delta":
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					Thinking    string `json:"thinking"`
					Signature   string `json:"signature"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return nil
			}
			acc := accs[ev.Index]
			if acc == nil {
				return nil
			}
			switch ev.Delta.Type {
			case "text_delta":
				acc.text.WriteString(ev.Delta.Text)
				return emit(Event{Type: EvTextDelta, Index: ev.Index, Text: ev.Delta.Text})
			case "thinking_delta":
				acc.text.WriteString(ev.Delta.Thinking)
				return emit(Event{Type: EvThinkingDelta, Index: ev.Index, Text: ev.Delta.Thinking})
			case "input_json_delta":
				acc.args.WriteString(ev.Delta.PartialJSON)
				return emit(Event{Type: EvToolDelta, Index: ev.Index, Text: ev.Delta.PartialJSON})
			case "signature_delta":
				acc.signature = ev.Delta.Signature
			}
			return nil
		case "content_block_stop":
			var ev struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return nil
			}
			acc := accs[ev.Index]
			if acc == nil {
				return nil
			}
			switch acc.kind {
			case "text":
				return emit(Event{Type: EvTextEnd, Index: ev.Index})
			case "thinking":
				return emit(Event{Type: EvThinkingEnd, Index: ev.Index})
			case "tool_use":
				return emit(Event{Type: EvToolEnd, Index: ev.Index, ToolID: acc.toolID, ToolName: acc.toolName})
			}
			return nil
		case "message_delta":
			var ev struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				if ev.Delta.StopReason != "" {
					stop = ev.Delta.StopReason
				}
				if ev.Usage.OutputTokens > 0 {
					usage.Output = ev.Usage.OutputTokens
				}
			}
			return nil
		case "message_stop":
			return errStreamDone
		case "error":
			var ev struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(data), &ev)
			return StreamError("anthropic", ev.Error.Type, ev.Error.Message)
		}
		return nil
	}

	pumpErr := pumpSSE(rctx, body, FirstEventTimeout, EventGapTimeout, handle)
	if pumpErr != nil {
		if pe, ok := AsProviderError(pumpErr); ok {
			return Result{}, pe
		}
		return Result{}, Classify(rctx, "anthropic", 0, "", 0, pumpErr)
	}

	// Assemble the assistant message.
	var blocks []Block
	var content []any
	for _, idx := range order {
		acc := accs[idx]
		if acc == nil {
			continue
		}
		switch acc.kind {
		case "text":
			blocks = append(blocks, Block{Type: BlockText, Text: acc.text.String()})
			content = append(content, map[string]any{"type": "text", "text": acc.text.String()})
		case "thinking":
			blocks = append(blocks, Block{Type: BlockThinking, Text: acc.text.String()})
			content = append(content, map[string]any{
				"type": "thinking", "thinking": acc.text.String(), "signature": acc.signature,
			})
		case "tool_use":
			args := json.RawMessage(acc.args.String())
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			blocks = append(blocks, Block{Type: BlockToolUse, ToolID: acc.toolID, ToolName: acc.toolName, Args: args})
			var input any = map[string]any{}
			_ = json.Unmarshal(args, &input)
			content = append(content, map[string]any{
				"type": "tool_use", "id": acc.toolID, "name": acc.toolName, "input": input,
			})
		}
	}
	if content == nil {
		content = []any{}
	}
	raw := RawEnvelope("anthropic", map[string]any{"role": "assistant", "content": content})
	res := Result{
		Message: Message{Role: "assistant", Blocks: blocks, Raw: raw},
		Usage:   usage,
		Raw:     raw,
		Stop:    mapAnthropicStop(stop),
	}
	if res.Stop == StopError {
		return res, &ProviderError{Class: ClassInvalidRequest, Message: fmt.Sprintf("stop reason %q", stop)}
	}
	return res, nil
}

func mapAnthropicStop(stop string) StopReason {
	switch stop {
	case "tool_use":
		return StopToolUse
	case "max_tokens", "model_context_window_exceeded":
		return StopLength
	case "refusal", "sensitive":
		return StopError
	case "", "end_turn", "stop_sequence", "pause_turn", "compaction":
		return StopEnd
	}
	return StopEnd
}
