package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/store"
)

func sseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, line := range strings.Split(body, "\n") {
			_, _ = fmt.Fprint(w, line+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type eventLog struct {
	events []Event
	text   strings.Builder
}

func (l *eventLog) emit(ev Event) error {
	l.events = append(l.events, ev)
	if ev.Type == EvTextDelta {
		l.text.WriteString(ev.Text)
	}
	return nil
}

func (l *eventLog) types() []string {
	out := make([]string, 0, len(l.events))
	for _, e := range l.events {
		out = append(out, e.Type.String())
	}
	return out
}

func TestAnthropicStream(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	}, "\n") + "\n"
	srv := sseServer(t, http.StatusOK, body)

	c := &anthropicClient{name: "anthropic", base: srv.URL, key: "k", http: HTTPClient()}
	log := &eventLog{}
	res, err := c.Stream(context.Background(), Request{
		Model: "m", MaxTokens: 100,
		System:   []Block{{Type: BlockText, Text: "sys"}},
		Messages: []Message{{Role: "user", Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		Tools:    []ToolSpec{{Name: "bash", Description: "run", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, log.emit)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := log.text.String(); got != "hello " {
		t.Fatalf("text = %q", got)
	}
	if res.Usage.Input != 11 || res.Usage.Output != 7 || res.Usage.CacheRead != 3 || res.Usage.CacheWrite != 2 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.Stop != StopToolUse {
		t.Fatalf("stop = %v", res.Stop)
	}
	calls := ToolCalls(res.Message)
	if len(calls) != 1 || calls[0].ToolName != "bash" || string(calls[0].Args) != `{"command":"ls"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
	payload, ok := RawFor(res.Raw, "anthropic")
	if !ok {
		t.Fatal("raw payload missing")
	}
	var msg struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("raw decode: %v", err)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("raw content = %+v", msg.Content)
	}
	// Replaying the raw message must produce the same wire content.
	encoded, err := c.encodeContent(Message{Role: "assistant", Raw: res.Raw})
	if err != nil {
		t.Fatalf("encodeContent: %v", err)
	}
	if !strings.Contains(string(encoded), `"tool_use"`) {
		t.Fatalf("replayed content = %s", encoded)
	}
}

func TestChatStream(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi "}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":2}}}`,
		``,
		`data: [DONE]`,
	}, "\n") + "\n"
	srv := sseServer(t, http.StatusOK, body)

	c := &chatClient{name: "openai", base: srv.URL, key: "k", http: HTTPClient()}
	log := &eventLog{}
	res, err := c.Stream(context.Background(), Request{
		Model: "m", MaxTokens: 50, Messages: []Message{{Role: "user", Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	}, log.emit)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if log.text.String() != "hi " {
		t.Fatalf("text = %q", log.text.String())
	}
	calls := ToolCalls(res.Message)
	if len(calls) != 1 || string(calls[0].Args) != `{"command":"ls"}` || calls[0].ToolID != "call_1" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if res.Stop != StopToolUse {
		t.Fatalf("stop = %v", res.Stop)
	}
	if res.Usage.Input != 9 || res.Usage.Output != 4 || res.Usage.CacheRead != 2 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if _, ok := RawFor(res.Raw, "openai-chat"); !ok {
		t.Fatal("raw payload wire mismatch")
	}
}

func TestResponsesStream(t *testing.T) {
	body := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"output_index":0,"item":{"type":"message","id":"msg_1"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"output_index":0,"delta":"hello"}`,
		``,
		`event: response.output_item.done`,
		`data: {"output_index":0,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"hello"}]}}`,
		``,
		`event: response.output_item.added`,
		`data: {"output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_9","name":"bash","arguments":""}}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"output_index":1,"delta":"{\"command\":\"pwd\"}"}`,
		``,
		`event: response.output_item.done`,
		`data: {"output_index":1,"item":{"type":"function_call","call_id":"call_9","name":"bash","arguments":"{\"command\":\"pwd\"}"}}`,
		``,
		`event: response.completed`,
		`data: {"response":{"usage":{"input_tokens":5,"output_tokens":6}}}`,
	}, "\n") + "\n"
	srv := sseServer(t, http.StatusOK, body)

	c := &responsesClient{name: "openrouter", base: srv.URL, key: "k", http: HTTPClient()}
	log := &eventLog{}
	res, err := c.Stream(context.Background(), Request{
		Model: "m", MaxTokens: 50, SessionID: "ses_1",
		Messages: []Message{{Role: "user", Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	}, log.emit)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if log.text.String() != "hello" {
		t.Fatalf("text = %q", log.text.String())
	}
	calls := ToolCalls(res.Message)
	if len(calls) != 1 || calls[0].ToolID != "call_9" || string(calls[0].Args) != `{"command":"pwd"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
	if res.Usage.Input != 5 || res.Usage.Output != 6 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.Stop != StopToolUse {
		t.Fatalf("stop = %v", res.Stop)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   Class
	}{
		{http.StatusUnauthorized, "nope", ClassAuth},
		{http.StatusTooManyRequests, "slow down", ClassRateLimit},
		{529, "overloaded", ClassOverloaded},
		{400, "prompt is too long: maximum context", ClassContextOverflow},
		{404, "not found", ClassUnsupported},
		{500, "boom", ClassServer},
		{422, "bad", ClassInvalidRequest},
	}
	for _, c := range cases {
		pe := Classify(context.Background(), "anthropic", c.status, c.body, 0, nil)
		if pe.Class != c.want {
			t.Errorf("status %d body %q → %s, want %s", c.status, c.body, pe.Class, c.want)
		}
	}
	pe := Classify(context.Background(), "anthropic", 0, "", 0, context.DeadlineExceeded)
	if pe.Class != ClassTimeout {
		t.Fatalf("deadline → %s, want timeout", pe.Class)
	}
}

func TestRunnerFailover(t *testing.T) {
	ok := sseServer(t, http.StatusOK, "data: [DONE]\n")
	var hits int
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer limited.Close()

	dir := t.TempDir()
	cfg := config.MockConfig(dir + "/config.toml")
	cfg.Providers["bad"] = config.Provider{Kind: "openai-chat", BaseURL: limited.URL, APIKeyEnv: "PIG_TEST_KEY"}
	cfg.Providers["good"] = config.Provider{Kind: "openai-chat", BaseURL: ok.URL, APIKeyEnv: "PIG_TEST_KEY"}
	cfg.Models = []config.Model{
		{Ref: "bad/m", Provider: "bad", Model: "m", ContextWindow: 1000, MaxOutput: 100},
		{Ref: "good/m", Provider: "good", Model: "m", ContextWindow: 1000, MaxOutput: 100},
	}
	cfg.Roles = config.Roles{Main: []string{"bad/m", "good/m"}}
	cfg.Failover.MaxAttempts = 3
	cfg.Failover.CooldownSeconds = 60
	t.Setenv("PIG_TEST_KEY", "x")

	st, err := store.Open(dir+"/pig.db", dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	reg := NewRegistry(cfg)
	runner := NewRunner(cfg, reg, st)
	chain := runner.Chain("", "main")
	if len(chain) != 2 {
		t.Fatalf("chain = %v", chain)
	}
	_, out, err := runner.Stream(context.Background(), "main", chain, Request{
		Model: "m", MaxTokens: 10, SessionID: "ses_x",
		Messages: []Message{{Role: "user", Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
	}, func(Event) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if out.Ref != "good/m" {
		t.Fatalf("answered by %q, want good/m", out.Ref)
	}
	if hits != 1 {
		t.Fatalf("bad model hit %d times", hits)
	}
	// The failed model must be in cooldown and skipped next time.
	chain2 := runner.Chain("", "main")
	if chain2[0] != "good/m" {
		t.Fatalf("sticky order = %v, want the healthy model first", chain2)
	}
	if !runner.cooling("bad/m") {
		t.Fatal("bad/m is not cooling down")
	}
	_ = time.Now()
}
