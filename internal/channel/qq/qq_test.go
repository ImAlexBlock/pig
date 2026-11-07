package qq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/config"
)

func TestTokenFetchAndCache(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/getAppAccessToken" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-123",
			"expires_in":   "7200",
		})
	}))
	defer srv.Close()

	tok := &Token{APIBase: srv.URL, AppID: "app", AppSecret: "secret"}
	got, err := tok.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "tok-123" {
		t.Fatalf("token = %q", got)
	}
	again, err := tok.Get(context.Background())
	if err != nil || again != got {
		t.Fatalf("cached Get = %q, %v", again, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("token endpoint called %d times, want 1", calls)
	}
}

func TestTokenFallsBack(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	tok := &Token{APIBase: bad.URL, AppID: "app", AppSecret: "secret"}
	if _, err := tok.Get(context.Background()); err == nil {
		t.Fatal("expected the fallback to fail as well")
	}
}

func TestSendDirectMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
			return
		}
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = map[string]any{}
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg-1"})
	}))
	defer srv.Close()

	ad := New(config.QQChannel{AppID: "app", APIBase: srv.URL, Markdown: true}, "secret")
	h, err := ad.Send(context.Background(), channel.Outbound{ChatID: "u:openid-1", Text: "hi", ReplyTo: "src"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/v2/users/openid-1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["msg_id"] != "src" {
		t.Fatalf("msg_id = %v", gotBody["msg_id"])
	}
	if seq, _ := gotBody["msg_seq"].(float64); seq < 1 {
		t.Fatalf("msg_seq = %v", gotBody["msg_seq"])
	}
	if h.ID != "msg-1" {
		t.Fatalf("handle = %q", h.ID)
	}
}

func TestSendGroupMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
			return
		}
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = map[string]any{}
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "g-msg"})
	}))
	defer srv.Close()
	ad := New(config.QQChannel{AppID: "app", APIBase: srv.URL, Markdown: true}, "secret")
	if _, err := ad.Send(context.Background(), channel.Outbound{ChatID: "g:group-1", Text: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/v2/groups/group-1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if mode, _ := gotBody["msg_type"].(float64); mode != 0 {
		t.Fatalf("group message used msg_type %v, want 0", gotBody["msg_type"])
	}
}

func TestDedupeAndMarkSeen(t *testing.T) {
	ad := New(config.QQChannel{AppID: "app"}, "secret")
	if !ad.markSeen("a") {
		t.Fatal("first sight should be new")
	}
	if ad.markSeen("a") {
		t.Fatal("duplicate accepted")
	}
}

func TestHandleEvent(t *testing.T) {
	ad := New(config.QQChannel{AppID: "app"}, "secret")
	in := make(chan channel.Inbound, 2)
	raw := []byte(`{"id":"m1","content":"hello","author":{"user_openid":"u1"},"group_openid":""}`)
	if err := ad.handleEvent(context.Background(), "C2C_MESSAGE_CREATE", raw, in); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	select {
	case got := <-in:
		if got.ChatID != "u:u1" || !strings.EqualFold(got.Text, "hello") {
			t.Fatalf("inbound = %+v", got)
		}
	default:
		t.Fatal("no inbound event")
	}
	group := []byte(`{"id":"m2","content":"group hi","author":{"member_openid":"m1"},"group_openid":"g1"}`)
	if err := ad.handleEvent(context.Background(), "GROUP_AT_MESSAGE_CREATE", group, in); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	select {
	case got := <-in:
		if got.ChatID != "g:g1" || !got.IsGroup {
			t.Fatalf("group inbound = %+v", got)
		}
	default:
		t.Fatal("no group inbound event")
	}
}
