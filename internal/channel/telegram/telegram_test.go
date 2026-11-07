package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexblock/pig/internal/channel"
)

func newTestAdapter(t *testing.T, handler func(method string, body map[string]any) any) (*Adapter, *[]string) {
	t.Helper()
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/botTOKEN/"), "/")
		method := parts[len(parts)-1]
		methods = append(methods, method)
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		res := handler(method, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
	}))
	t.Cleanup(srv.Close)
	a := New("TOKEN")
	a.base = srv.URL
	return a, &methods
}

func TestSendAndUpdate(t *testing.T) {
	ad, methods := newTestAdapter(t, func(method string, body map[string]any) any {
		if method == "sendMessage" {
			return map[string]any{"message_id": 42}
		}
		return true
	})
	ctx := context.Background()
	h, err := ad.Send(ctx, channel.Outbound{ChatID: "1", Text: "hello", Markdown: true})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if h.ID != "42" {
		t.Fatalf("handle = %q", h.ID)
	}
	if err := ad.Update(ctx, h, channel.Outbound{ChatID: "1", Text: "done"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := *methods
	if len(got) != 2 || got[0] != "sendMessage" || got[1] != "editMessageText" {
		t.Fatalf("methods = %v", got)
	}
}

func TestSendSplitsLongText(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": len(bodies)}})
	}))
	defer srv.Close()
	ad := New("TOKEN")
	ad.base = srv.URL
	long := strings.Repeat("a", ad.Caps().MaxChars+50)
	if _, err := ad.Send(context.Background(), channel.Outbound{ChatID: "1", Text: long}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("sent %d messages, want 2", len(bodies))
	}
	for _, b := range bodies {
		if b["chat_id"] != "1" {
			t.Fatalf("chat_id = %v", b["chat_id"])
		}
		if text, _ := b["text"].(string); len(text) > ad.Caps().MaxChars {
			t.Fatalf("chunk of %d chars", len(text))
		}
	}
}

func TestRunDeliversInbound(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{
					"update_id": 7,
					"message": map[string]any{
						"message_id": 3,
						"text":       "ping",
						"chat":       map[string]any{"id": 99, "type": "private"},
						"from":       map[string]any{"id": 5},
					},
				},
			}})
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	ad := New("TOKEN")
	ad.base = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := make(chan channel.Inbound, 4)
	go func() { _ = ad.Run(ctx, in) }()
	select {
	case got := <-in:
		if got.Text != "ping" || got.ChatID != "99" || got.UserID != "5" || got.MessageID != "3" {
			t.Fatalf("inbound = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("no inbound message")
	}
}
