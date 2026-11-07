package provider

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// MockScript drives the mock provider from PIG_MOCK_SCRIPT.
type MockScript struct {
	Turns     []MockTurn `json:"turns"`
	Summarize string     `json:"summarize"`
}

// MockTurn is one scripted assistant turn.
type MockTurn struct {
	Text    string        `json:"text"`
	Tool    *MockToolCall `json:"tool"`
	DelayMS int           `json:"delay_ms"`
}

// MockToolCall is a scripted tool invocation.
type MockToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type mockClient struct {
	script *MockScript
}

func newMockClient() *mockClient {
	c := &mockClient{}
	if v := os.Getenv("PIG_MOCK_SCRIPT"); v != "" {
		var s MockScript
		if err := json.Unmarshal([]byte(v), &s); err == nil {
			c.script = &s
		}
	}
	return c
}

func (c *mockClient) Kind() string { return "mock" }

func systemText(system []Block) string {
	var sb strings.Builder
	for _, b := range system {
		if b.Type == BlockText {
			sb.WriteString(b.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func lastUser(msgs []Message) (text string, afterTool bool, assistantTurns int, toolText string) {
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistantTurns++
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "user" {
			continue
		}
		var sb strings.Builder
		toolResult := false
		last := ""
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				sb.WriteString(b.Text)
			case BlockToolResult:
				toolResult = true
				last = b.Text
			}
		}
		return sb.String(), toolResult, assistantTurns, last
	}
	return "", false, assistantTurns, ""
}

func (c *mockClient) Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error) {
	usage := Usage{Input: 100, Output: 10}

	text, afterTool, turns, toolText := lastUser(req.Messages)

	if strings.Contains(strings.ToLower(systemText(req.System)), "summarize the conversation") {
		summary := "mock summary"
		if c.script != nil && c.script.Summarize != "" {
			summary = c.script.Summarize
		}
		return c.textResult(emit, summary, usage)
	}

	if c.script != nil && len(c.script.Turns) > 0 {
		idx := turns
		if idx >= len(c.script.Turns) {
			idx = len(c.script.Turns) - 1
		}
		turn := c.script.Turns[idx]
		if turn.DelayMS > 0 {
			select {
			case <-ctx.Done():
				return Result{}, &ProviderError{Class: ClassCanceled, Message: "canceled"}
			case <-time.After(time.Duration(turn.DelayMS) * time.Millisecond):
			}
		}
		if turn.Tool != nil {
			args := turn.Tool.Args
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			return c.toolResult(emit, turn.Text, turn.Tool.Name, args, usage)
		}
		return c.textResult(emit, turn.Text, usage)
	}

	switch {
	case afterTool:
		reply := "tool ok"
		if first := firstLineText(toolText); first != "" {
			reply = "tool ok: " + first
		}
		return c.textResult(emit, reply, usage)
	case strings.HasPrefix(text, "tool:"):
		cmd := strings.TrimSpace(strings.TrimPrefix(text, "tool:"))
		args, _ := json.Marshal(map[string]string{"command": cmd})
		return c.toolResult(emit, "", "bash", args, usage)
	case strings.HasPrefix(text, "spawn:"):
		prompt := strings.TrimSpace(strings.TrimPrefix(text, "spawn:"))
		args, _ := json.Marshal(map[string]string{"prompt": prompt})
		return c.toolResult(emit, "", "task", args, usage)
	case strings.HasPrefix(text, "slow:"):
		select {
		case <-ctx.Done():
			return Result{}, &ProviderError{Class: ClassCanceled, Message: "canceled"}
		case <-time.After(500 * time.Millisecond):
		}
		return c.textResult(emit, "mock: "+text, usage)
	}
	return c.textResult(emit, "mock: "+text, usage)
}

func firstLineText(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (c *mockClient) textResult(emit func(Event) error, text string, usage Usage) (Result, error) {
	if err := emit(Event{Type: EvTextStart}); err != nil {
		return Result{}, err
	}
	if text != "" {
		if err := emit(Event{Type: EvTextDelta, Text: text}); err != nil {
			return Result{}, err
		}
	}
	if err := emit(Event{Type: EvTextEnd}); err != nil {
		return Result{}, err
	}
	raw := RawEnvelope("mock", map[string]any{"role": "assistant", "content": text})
	return Result{
		Message: Message{Role: "assistant", Blocks: []Block{{Type: BlockText, Text: text}}, Raw: raw},
		Usage:   usage,
		Stop:    StopEnd,
		Raw:     raw,
	}, nil
}

func (c *mockClient) toolResult(emit func(Event) error, text, name string, args json.RawMessage, usage Usage) (Result, error) {
	var blocks []Block
	if text != "" {
		if err := emit(Event{Type: EvTextStart}); err != nil {
			return Result{}, err
		}
		if err := emit(Event{Type: EvTextDelta, Text: text}); err != nil {
			return Result{}, err
		}
		if err := emit(Event{Type: EvTextEnd}); err != nil {
			return Result{}, err
		}
		blocks = append(blocks, Block{Type: BlockText, Text: text})
	}
	const callID = "call_mock_1"
	if err := emit(Event{Type: EvToolStart, Index: 0, ToolID: callID, ToolName: name}); err != nil {
		return Result{}, err
	}
	if len(args) > 0 {
		if err := emit(Event{Type: EvToolDelta, Index: 0, Text: string(args)}); err != nil {
			return Result{}, err
		}
	}
	if err := emit(Event{Type: EvToolEnd, Index: 0, ToolID: callID, ToolName: name}); err != nil {
		return Result{}, err
	}
	blocks = append(blocks, Block{Type: BlockToolUse, ToolID: callID, ToolName: name, Args: args})
	raw := RawEnvelope("mock", map[string]any{
		"role": "assistant", "content": text, "tool": map[string]any{"name": name, "args": json.RawMessage(args)},
	})
	return Result{
		Message: Message{Role: "assistant", Blocks: blocks, Raw: raw},
		Usage:   usage,
		Stop:    StopToolUse,
		Raw:     raw,
	}, nil
}
