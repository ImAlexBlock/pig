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

	"github.com/ImAlexBlock/pig/internal/channel"
	"github.com/ImAlexBlock/pig/internal/config"
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

func TestSendMediaUploadFlow(t *testing.T) {
	var uploads, messages int
	var lastUpload, lastMsg map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		switch {
		case strings.HasPrefix(r.URL.Path, "/app/getAppAccessToken"):
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
		case strings.HasSuffix(r.URL.Path, "/files"):
			uploads++
			lastUpload = body
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "FI/abc"})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			messages++
			lastMsg = body
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ad := New(config.QQChannel{AppID: "app", APIBase: srv.URL}, "secret")
	err := ad.SendMedia(context.Background(), "u:openid-1",
		[]channel.File{{Name: "shot.png", Mime: "image/png", Data: []byte("PNGDATA")}}, "src-msg")
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	if uploads != 1 || messages != 1 {
		t.Fatalf("uploads=%d messages=%d", uploads, messages)
	}
	if lastUpload["file_type"] != float64(1) {
		t.Fatalf("file_type = %v, want 1 (image)", lastUpload["file_type"])
	}
	if lastUpload["srv_send_msg"] != false {
		t.Fatalf("srv_send_msg = %v, want false", lastUpload["srv_send_msg"])
	}
	if lastMsg["msg_type"] != float64(7) {
		t.Fatalf("msg_type = %v, want 7", lastMsg["msg_type"])
	}
	media, _ := lastMsg["media"].(map[string]any)
	if media["file_info"] != "FI/abc" {
		t.Fatalf("media = %v", lastMsg["media"])
	}
	if lastMsg["msg_id"] != "src-msg" {
		t.Fatalf("msg_id = %v", lastMsg["msg_id"])
	}
	if _, ok := lastMsg["msg_seq"]; !ok {
		t.Fatal("msg_seq missing")
	}
}

func TestInboundAttachmentDownload(t *testing.T) {
	var authHeader string
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("content-type", "image/png")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer media.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
	}))
	defer srv.Close()

	ad := New(config.QQChannel{AppID: "app", APIBase: srv.URL}, "secret")
	in := make(chan channel.Inbound, 1)
	raw := []byte(`{"id":"m1","content":"","author":{"user_openid":"u1"},"attachments":[{"content_type":"image/png","url":"` + media.URL + `/a.png","filename":"a.png","size":7}]}`)
	if err := ad.handleEvent(context.Background(), "C2C_MESSAGE_CREATE", raw, in); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}
	select {
	case got := <-in:
		if len(got.Attachments) != 1 {
			t.Fatalf("attachments = %d", len(got.Attachments))
		}
		att := got.Attachments[0]
		if att.Kind != "image" || att.Mime != "image/png" || string(att.Data) != "PNGDATA" || att.Name != "a.png" {
			t.Fatalf("attachment = %+v", att)
		}
		if authHeader != "QQBot tok" {
			t.Fatalf("authorization = %q", authHeader)
		}
	default:
		t.Fatal("no inbound event")
	}
}

func TestRemainingSlots(t *testing.T) {
	a := New(config.QQChannel{AppID: "1", APIBase: "https://api.bot.qq.com"}, "s")
	if got := a.RemainingSlots(PrefixUser+"u", "m1"); got != PassiveReplyLimit {
		t.Fatalf("fresh message slots = %d, want %d", got, PassiveReplyLimit)
	}
	for i := 1; i <= 3; i++ {
		if seq, ok := a.nextSeq("m1"); !ok || seq != i {
			t.Fatalf("nextSeq = %d, %v at step %d", seq, ok, i)
		}
	}
	if got := a.RemainingSlots(PrefixUser+"u", "m1"); got != 2 {
		t.Fatalf("after 3 replies slots = %d, want 2", got)
	}
	for i := 0; i < 2; i++ {
		if _, ok := a.nextSeq("m1"); !ok {
			t.Fatalf("reply %d rejected", i+4)
		}
	}
	if got := a.RemainingSlots(PrefixUser+"u", "m1"); got != 0 {
		t.Fatalf("exhausted slots = %d, want 0", got)
	}
	if _, ok := a.nextSeq("m1"); ok {
		t.Fatal("sixth passive reply accepted")
	}
}

// Text mode is append-only: advertising an edit would make the daemon post a
// new bubble for every progress tick.
func TestTextModeCannotEdit(t *testing.T) {
	text := New(config.QQChannel{AppID: "1", APIBase: "https://api.bot.qq.com"}, "s")
	if text.Caps().Edit {
		t.Fatal("text mode must not advertise editing")
	}
	md := New(config.QQChannel{AppID: "1", APIBase: "https://api.bot.qq.com", Markdown: true}, "s")
	if !md.Caps().Edit {
		t.Fatal("markdown mode should advertise editing")
	}
}

func TestUpdateNeverSendsInTextMode(t *testing.T) {
	var sends int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "/messages") || strings.Contains(r.URL.Path, "/stream_messages") {
			mu.Lock()
			sends++
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":600}`))
	}))
	defer srv.Close()

	a := New(config.QQChannel{AppID: "1", APIBase: srv.URL}, "sec")
	_ = a.Update(context.Background(), channel.Handle{ID: "h1"},
		channel.Outbound{ChatID: PrefixUser + "u", Text: "x", ReplyTo: "m1", Markdown: false})

	mu.Lock()
	defer mu.Unlock()
	if sends != 0 {
		t.Fatalf("Update posted %d message request(s) in text mode", sends)
	}
}
