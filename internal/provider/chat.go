package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type chatClient struct {
	name string
	base string
	key  string
	http *http.Client
}

func (c *chatClient) Kind() string { return "openai-chat" }

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatRequest struct {
	Model           string            `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	Tools           []chatTool        `json:"tools,omitempty"`
	Stream          bool              `json:"stream"`
	StreamOptions   map[string]any    `json:"stream_options,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	MaxCompletionTk int               `json:"max_completion_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Seed            *int64            `json:"seed,omitempty"`
}

func (c *chatClient) buildBody(req Request) ([]byte, error) {
	out := chatRequest{Model: req.Model, Stream: true, StreamOptions: map[string]any{"include_usage": true}}
	out.Temperature = req.Temperature
	out.TopP = req.TopP
	out.Seed = req.Seed
	if req.MaxTokensField == "max_completion_tokens" {
		out.MaxCompletionTk = req.MaxTokens
	} else {
		out.MaxTokens = req.MaxTokens
	}
	var sys strings.Builder
	for _, b := range req.System {
		if b.Type == BlockText && b.Text != "" {
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(b.Text)
		}
	}
	if sys.Len() > 0 {
		msg, _ := json.Marshal(map[string]any{"role": "system", "content": sys.String()})
		out.Messages = append(out.Messages, msg)
	}
	for _, t := range req.Tools {
		ct := chatTool{Type: "function"}
		ct.Function.Name = t.Name
		ct.Function.Description = t.Description
		ct.Function.Parameters = t.Schema
		out.Tools = append(out.Tools, ct)
	}
	for _, m := range MergeMessages(req.Messages) {
		encoded, err := c.encodeMessage(m)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, encoded...)
	}
	return json.Marshal(out)
}

func (c *chatClient) encodeMessage(m Message) ([]json.RawMessage, error) {
	if m.Role == "assistant" {
		if payload, ok := RawFor(m.Raw, "openai-chat"); ok {
			return []json.RawMessage{payload}, nil
		}
	}
	switch m.Role {
	case "assistant":
		var text strings.Builder
		var calls []chatToolCall
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				text.WriteString(b.Text)
			case BlockToolUse:
				call := chatToolCall{ID: b.ToolID, Type: "function"}
				call.Function.Name = b.ToolName
				call.Function.Arguments = string(b.Args)
				if call.Function.Arguments == "" {
					call.Function.Arguments = "{}"
				}
				calls = append(calls, call)
			}
		}
		obj := map[string]any{"role": "assistant"}
		if text.Len() > 0 {
			obj["content"] = text.String()
		} else {
			obj["content"] = nil
		}
		if len(calls) > 0 {
			obj["tool_calls"] = calls
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		return []json.RawMessage{b}, nil
	default:
		var out []json.RawMessage
		// One message per tool result, plus text/images for user content.
		var parts []any
		var texts []string
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				if b.Text != "" {
					texts = append(texts, b.Text)
					parts = append(parts, map[string]any{"type": "text", "text": b.Text})
				}
			case BlockImage:
				if b.Image == nil {
					continue
				}
				url := "data:" + b.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(b.Image.Data)
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			case BlockToolResult:
				obj := map[string]any{
					"role":         "tool",
					"tool_call_id": b.ToolID,
					"content":      b.Text,
				}
				raw, err := json.Marshal(obj)
				if err != nil {
					return nil, err
				}
				out = append(out, raw)
			}
		}
		if len(texts) > 0 || containsImageBlock(m.Blocks) {
			var content any
			if containsImageBlock(m.Blocks) {
				content = parts
			} else {
				content = strings.Join(texts, "\n")
			}
			obj := map[string]any{"role": "user", "content": content}
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			out = append(out, raw)
		}
		if len(out) == 0 {
			obj := map[string]any{"role": "user", "content": ""}
			raw, _ := json.Marshal(obj)
			out = append(out, raw)
		}
		return out, nil
	}
}

func containsImageBlock(blocks []Block) bool {
	for _, b := range blocks {
		if b.Type == BlockImage && b.Image != nil {
			return true
		}
	}
	return false
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        *string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptCacheHit      int `json:"prompt_cache_hit_tokens"`
		PromptCacheMiss     int `json:"prompt_cache_miss_tokens"`
		PromptTokensDetails *struct {
			CachedTokens        int `json:"cached_tokens"`
			CacheCreationTokens int `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *chatClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error) {
	payload, err := c.buildBody(req)
	if err != nil {
		return Result{}, &ProviderError{Class: ClassInvalidRequest, Message: err.Error()}
	}
	rctx, cancel := context.WithTimeout(ctx, WholeRequestTimeout)
	defer cancel()
	body, pe := postStream(rctx, c.http, c.base+"/chat/completions", map[string]string{
		"Authorization": "Bearer " + c.key,
	}, payload)
	if pe != nil {
		return Result{}, pe
	}

	var usage Usage
	var text strings.Builder
	var finish string
	type toolAcc struct {
		id, name string
		args     strings.Builder
		started  bool
	}
	tools := map[int]*toolAcc{}
	var toolOrder []int
	textStarted := false
	thinkStarted := false
	done := false

	handle := func(event, data string) error {
		if strings.TrimSpace(data) == "[DONE]" {
			done = true
			return errStreamDone
		}
		var ch chatChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return nil
		}
		if ch.Error != nil {
			return StreamError("openai-chat", ch.Error.Type, ch.Error.Message)
		}
		if ch.Usage != nil {
			usage.Input = ch.Usage.PromptTokens
			usage.Output = ch.Usage.CompletionTokens
			if ch.Usage.PromptTokensDetails != nil {
				usage.CacheRead = ch.Usage.PromptTokensDetails.CachedTokens
				usage.CacheWrite = ch.Usage.PromptTokensDetails.CacheCreationTokens
			}
			// DeepSeek-style reports put cache reads in a separate field that
			// OpenAI-shaped `cached_tokens` does not carry.
			if ch.Usage.PromptCacheHit > usage.CacheRead {
				usage.CacheRead = ch.Usage.PromptCacheHit
			}
		}
		for _, choice := range ch.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
			d := choice.Delta
			if d.ReasoningContent != nil && *d.ReasoningContent != "" {
				if !thinkStarted {
					thinkStarted = true
					if err := emit(Event{Type: EvThinkingStart}); err != nil {
						return err
					}
				}
				if err := emit(Event{Type: EvThinkingDelta, Text: *d.ReasoningContent}); err != nil {
					return err
				}
			}
			if d.Reasoning != nil && *d.Reasoning != "" {
				if !thinkStarted {
					thinkStarted = true
					if err := emit(Event{Type: EvThinkingStart}); err != nil {
						return err
					}
				}
				if err := emit(Event{Type: EvThinkingDelta, Text: *d.Reasoning}); err != nil {
					return err
				}
			}
			if d.Content != nil && *d.Content != "" {
				if !textStarted {
					textStarted = true
					if err := emit(Event{Type: EvTextStart}); err != nil {
						return err
					}
				}
				text.WriteString(*d.Content)
				if err := emit(Event{Type: EvTextDelta, Text: *d.Content}); err != nil {
					return err
				}
			}
			for _, tc := range d.ToolCalls {
				acc := tools[tc.Index]
				if acc == nil {
					acc = &toolAcc{}
					tools[tc.Index] = acc
					toolOrder = append(toolOrder, tc.Index)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				if !acc.started {
					acc.started = true
					if err := emit(Event{Type: EvToolStart, Index: tc.Index, ToolID: acc.id, ToolName: acc.name}); err != nil {
						return err
					}
				}
				if tc.Function.Arguments != "" {
					acc.args.WriteString(tc.Function.Arguments)
					if err := emit(Event{Type: EvToolDelta, Index: tc.Index, Text: tc.Function.Arguments}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	pumpErr := pumpSSE(rctx, body, FirstEventTimeout, EventGapTimeout, handle)
	if pumpErr != nil {
		if pe, ok := AsProviderError(pumpErr); ok {
			return Result{}, pe
		}
		return Result{}, Classify(rctx, "openai-chat", 0, "", 0, pumpErr)
	}
	_ = done

	var blocks []Block
	if textStarted {
		blocks = append(blocks, Block{Type: BlockText, Text: text.String()})
	}
	wireCalls := make([]chatToolCall, 0, len(toolOrder))
	for _, idx := range toolOrder {
		acc := tools[idx]
		args := acc.args.String()
		if args == "" {
			args = "{}"
		}
		blocks = append(blocks, Block{
			Type: BlockToolUse, ToolID: acc.id, ToolName: acc.name, Args: json.RawMessage(args),
		})
		call := chatToolCall{ID: acc.id, Type: "function"}
		call.Function.Name = acc.name
		call.Function.Arguments = args
		wireCalls = append(wireCalls, call)
		if err := emit(Event{Type: EvToolEnd, Index: idx, ToolID: acc.id, ToolName: acc.name}); err != nil {
			return Result{}, err
		}
	}
	if textStarted {
		if err := emit(Event{Type: EvTextEnd}); err != nil {
			return Result{}, err
		}
	}

	wireMsg := map[string]any{"role": "assistant"}
	if textStarted {
		wireMsg["content"] = text.String()
	} else {
		wireMsg["content"] = nil
	}
	if len(wireCalls) > 0 {
		wireMsg["tool_calls"] = wireCalls
	}
	raw := RawEnvelope("openai-chat", wireMsg)
	res := Result{
		Message: Message{Role: "assistant", Blocks: blocks, Raw: raw},
		Usage:   usage,
		Stop:    mapChatStop(finish),
		Raw:     raw,
	}
	if res.Stop == StopError {
		return res, &ProviderError{Class: ClassInvalidRequest, Message: fmt.Sprintf("finish reason %q", finish)}
	}
	return res, nil
}

func mapChatStop(finish string) StopReason {
	switch finish {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length", "max_tokens":
		return StopLength
	case "content_filter":
		return StopError
	}
	return StopEnd
}
