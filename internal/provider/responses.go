package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

type responsesClient struct {
	name string
	base string
	key  string
	http *http.Client
}

func (c *responsesClient) Kind() string { return "openai-responses" }

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesRequest struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []responsesTool   `json:"tools,omitempty"`
	Stream          bool              `json:"stream"`
	Store           bool              `json:"store"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	PromptCacheKey  string            `json:"prompt_cache_key,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
}

func (c *responsesClient) buildBody(req Request) ([]byte, error) {
	out := responsesRequest{Model: req.Model, Stream: true, Store: false, MaxOutputTokens: req.MaxTokens,
		PromptCacheKey: req.SessionID}
	var sys strings.Builder
	for _, b := range req.System {
		if b.Type == BlockText && b.Text != "" {
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(b.Text)
		}
	}
	out.Instructions = sys.String()
	out.Temperature = req.Temperature
	out.TopP = req.TopP
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, responsesTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	for _, m := range MergeMessages(req.Messages) {
		items, err := c.encodeMessage(m)
		if err != nil {
			return nil, err
		}
		out.Input = append(out.Input, items...)
	}
	return json.Marshal(out)
}

func (c *responsesClient) encodeMessage(m Message) ([]json.RawMessage, error) {
	if m.Role == "assistant" {
		if payload, ok := RawFor(m.Raw, "openai-responses"); ok {
			var items []json.RawMessage
			if err := json.Unmarshal(payload, &items); err == nil && len(items) > 0 {
				return items, nil
			}
		}
	}
	var out []json.RawMessage
	switch m.Role {
	case "assistant":
		var texts []string
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case BlockToolUse:
				args := string(b.Args)
				if args == "" {
					args = "{}"
				}
				obj := map[string]any{
					"type": "function_call", "call_id": b.ToolID, "name": b.ToolName, "arguments": args,
				}
				raw, err := json.Marshal(obj)
				if err != nil {
					return nil, err
				}
				out = append(out, raw)
			}
		}
		if len(texts) > 0 {
			obj := map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": strings.Join(texts, "\n")}},
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			out = append(out, raw)
		}
	default:
		var content []any
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				if b.Text != "" {
					content = append(content, map[string]any{"type": "input_text", "text": b.Text})
				}
			case BlockImage:
				if b.Image == nil {
					continue
				}
				url := "data:" + b.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(b.Image.Data)
				content = append(content, map[string]any{"type": "input_image", "image_url": url})
			case BlockToolResult:
				obj := map[string]any{
					"type": "function_call_output", "call_id": b.ToolID, "output": b.Text,
				}
				raw, err := json.Marshal(obj)
				if err != nil {
					return nil, err
				}
				out = append(out, raw)
			}
		}
		if content != nil {
			obj := map[string]any{"role": "user", "content": content}
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			out = append(out, raw)
		}
	}
	if len(out) == 0 {
		raw, _ := json.Marshal(map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": ""}}})
		out = append(out, raw)
	}
	return out, nil
}

type respItemAcc struct {
	kind    string
	id      string
	name    string
	callID  string
	text    strings.Builder
	args    strings.Builder
	started bool
}

func (c *responsesClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error) {
	payload, err := c.buildBody(req)
	if err != nil {
		return Result{}, &ProviderError{Class: ClassInvalidRequest, Message: err.Error()}
	}
	rctx, cancel := context.WithTimeout(ctx, WholeRequestTimeout)
	defer cancel()
	body, pe := postStream(rctx, c.http, c.base+"/responses", map[string]string{
		"Authorization": "Bearer " + c.key,
	}, payload)
	if pe != nil {
		return Result{}, pe
	}

	var usage Usage
	var stop StopReason = -1
	items := map[int]*respItemAcc{}
	var order []int
	var failure *ProviderError
	incomplete := ""

	handle := func(event, data string) error {
		var head struct {
			OutputIndex int             `json:"output_index"`
			Delta       string          `json:"delta"`
			Item        json.RawMessage `json:"item"`
			Response    struct {
				Usage *struct {
					InputTokens        int `json:"input_tokens"`
					OutputTokens       int `json:"output_tokens"`
					InputTokensDetails *struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &head); err != nil {
			return nil
		}

		itemOf := func(idx int) *respItemAcc {
			acc := items[idx]
			if acc == nil {
				acc = &respItemAcc{}
				items[idx] = acc
				order = append(order, idx)
			}
			return acc
		}

		switch event {
		case "response.output_item.added":
			var item struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			_ = json.Unmarshal(head.Item, &item)
			acc := itemOf(head.OutputIndex)
			acc.kind = item.Type
			acc.id = item.ID
			acc.name = item.Name
			acc.callID = item.CallID
			switch item.Type {
			case "function_call":
				acc.started = true
				return emit(Event{Type: EvToolStart, Index: head.OutputIndex, ToolID: item.CallID, ToolName: item.Name})
			case "message":
				acc.started = true
				return emit(Event{Type: EvTextStart, Index: head.OutputIndex})
			case "reasoning":
				return emit(Event{Type: EvThinkingStart, Index: head.OutputIndex})
			}
			return nil
		case "response.output_text.delta":
			acc := itemOf(head.OutputIndex)
			if !acc.started {
				acc.started = true
				acc.kind = "message"
				if err := emit(Event{Type: EvTextStart, Index: head.OutputIndex}); err != nil {
					return err
				}
			}
			acc.text.WriteString(head.Delta)
			return emit(Event{Type: EvTextDelta, Index: head.OutputIndex, Text: head.Delta})
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			acc := itemOf(head.OutputIndex)
			acc.kind = "reasoning"
			acc.text.WriteString(head.Delta)
			return emit(Event{Type: EvThinkingDelta, Index: head.OutputIndex, Text: head.Delta})
		case "response.function_call_arguments.delta":
			acc := itemOf(head.OutputIndex)
			acc.args.WriteString(head.Delta)
			return emit(Event{Type: EvToolDelta, Index: head.OutputIndex, Text: head.Delta})
		case "response.output_item.done":
			var item struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
				Content   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			_ = json.Unmarshal(head.Item, &item)
			acc := itemOf(head.OutputIndex)
			if item.Type != "" {
				acc.kind = item.Type
			}
			if item.Name != "" {
				acc.name = item.Name
			}
			if item.CallID != "" {
				acc.callID = item.CallID
			}
			if item.Arguments != "" && acc.args.Len() == 0 {
				acc.args.WriteString(item.Arguments)
			}
			if item.Type == "message" && acc.text.Len() == 0 {
				for _, c := range item.Content {
					if c.Type == "output_text" {
						acc.text.WriteString(c.Text)
					}
				}
			}
			switch item.Type {
			case "function_call":
				return emit(Event{Type: EvToolEnd, Index: head.OutputIndex, ToolID: acc.callID, ToolName: acc.name})
			case "message":
				return emit(Event{Type: EvTextEnd, Index: head.OutputIndex})
			}
			return nil
		case "response.completed":
			if head.Response.Usage != nil {
				usage.Input = head.Response.Usage.InputTokens
				usage.Output = head.Response.Usage.OutputTokens
				if head.Response.Usage.InputTokensDetails != nil {
					usage.CacheRead = head.Response.Usage.InputTokensDetails.CachedTokens
				}
			}
			return errStreamDone
		case "response.incomplete":
			if head.Response.Usage != nil {
				usage.Input = head.Response.Usage.InputTokens
				usage.Output = head.Response.Usage.OutputTokens
			}
			if head.Response.IncompleteDetails != nil {
				incomplete = head.Response.IncompleteDetails.Reason
			}
			return errStreamDone
		case "response.failed":
			msg := "response failed"
			code := ""
			if head.Response.Error != nil {
				if head.Response.Error.Message != "" {
					msg = head.Response.Error.Message
				}
				code = head.Response.Error.Code
			}
			failure = StreamError("openai-responses", code, msg)
			return errStreamDone
		case "error":
			failure = StreamError("openai-responses", head.Code, head.Message)
			return errStreamDone
		}
		return nil
	}

	pumpErr := pumpSSE(rctx, body, FirstEventTimeout, EventGapTimeout, handle)
	if pumpErr != nil {
		if pe, ok := AsProviderError(pumpErr); ok {
			return Result{}, pe
		}
		return Result{}, Classify(rctx, "openai-responses", 0, "", 0, pumpErr)
	}
	if failure != nil {
		return Result{}, failure
	}

	var blocks []Block
	var wireItems []any
	hasTool := false
	for _, idx := range order {
		acc := items[idx]
		switch acc.kind {
		case "message":
			blocks = append(blocks, Block{Type: BlockText, Text: acc.text.String()})
			wireItems = append(wireItems, map[string]any{
				"type": "message", "role": "assistant", "id": acc.id,
				"content": []any{map[string]any{"type": "output_text", "text": acc.text.String()}},
			})
		case "reasoning":
			blocks = append(blocks, Block{Type: BlockThinking, Text: acc.text.String()})
			wireItems = append(wireItems, map[string]any{
				"type": "reasoning", "id": acc.id,
				"summary": []any{map[string]any{"type": "summary_text", "text": acc.text.String()}},
			})
		case "function_call":
			args := acc.args.String()
			if args == "" {
				args = "{}"
			}
			hasTool = true
			blocks = append(blocks, Block{Type: BlockToolUse, ToolID: acc.callID, ToolName: acc.name,
				Args: json.RawMessage(args)})
			wireItems = append(wireItems, map[string]any{
				"type": "function_call", "call_id": acc.callID, "name": acc.name, "arguments": args,
			})
		}
	}
	if wireItems == nil {
		wireItems = []any{}
	}
	raw := RawEnvelope("openai-responses", wireItems)
	res := Result{
		Message: Message{Role: "assistant", Blocks: blocks, Raw: raw},
		Usage:   usage,
		Raw:     raw,
		Stop:    StopEnd,
	}
	if hasTool {
		res.Stop = StopToolUse
	}
	switch {
	case incomplete == "content_filter":
		return res, &ProviderError{Class: ClassInvalidRequest, Message: "content filter"}
	case incomplete != "":
		res.Stop = StopLength
	}
	if stop >= 0 {
		res.Stop = stop
	}
	return res, nil
}
