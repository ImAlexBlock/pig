package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/store"
)

type fakeAdapter struct {
	name     string
	markdown bool
	mu       sync.Mutex
	sent     []string
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Caps() channel.Caps {
	return channel.Caps{Edit: f.markdown, Stream: false, Markdown: f.markdown, MaxChars: 4096}
}

func (f *fakeAdapter) Run(ctx context.Context, in chan<- channel.Inbound) error {
	<-ctx.Done()
	return nil
}

func (f *fakeAdapter) Send(ctx context.Context, o channel.Outbound) (channel.Handle, error) {
	f.mu.Lock()
	f.sent = append(f.sent, o.Text)
	f.mu.Unlock()
	return channel.Handle{ID: "h1"}, nil
}

func (f *fakeAdapter) Update(ctx context.Context, h channel.Handle, o channel.Outbound) error {
	return nil
}

func (f *fakeAdapter) Close() error { return nil }

func (f *fakeAdapter) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeAdapter) waitFor(t *testing.T, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range f.messages() {
			if strings.Contains(m, substr) {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no message contained %q; got %v", substr, f.messages())
	return ""
}

func TestChannelPairingAndRun(t *testing.T) {
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)

	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Channel.Workdir = filepath.Join(home, "workdir")
	paths := config.Paths{Home: home}
	srv := startTestServer(t, cfg, paths)

	ad := &fakeAdapter{name: "fake"}
	srv.channels = []channel.Adapter{ad}
	pairing := channel.NewPairing("fake")
	srv.pairings["fake"] = pairing

	ctx := context.Background()
	chat := channel.Inbound{Channel: "fake", ChatID: "chat-1", UserID: "u1", MessageID: "m1"}

	// Unpaired chat must be refused.
	unpaired := chat
	unpaired.Text = "hello"
	srv.handleChannelMessage(ctx, ad, unpaired)
	ad.waitFor(t, "pairing required", 5*time.Second)

	// Wrong code is rejected.
	wrong := chat
	wrong.Text = "/pair 000000"
	if pairing.Code() == "000000" {
		wrong.Text = "/pair 111111"
	}
	srv.handleChannelMessage(ctx, ad, wrong)
	ad.waitFor(t, "pairing failed", 5*time.Second)

	// Correct code binds the chat.
	good := chat
	good.Text = "/pair " + pairing.Code()
	srv.handleChannelMessage(ctx, ad, good)
	ad.waitFor(t, "paired", 5*time.Second)

	state, err := srv.Store().GetChannelState("fake", "chat-1")
	if err != nil {
		t.Fatalf("GetChannelState: %v", err)
	}
	if !state.Paired || state.SessionID == "" {
		t.Fatalf("state = %+v", state)
	}
	sess, err := srv.Store().GetSession(state.SessionID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if sess.CWD != cfg.Channel.Workdir {
		t.Fatalf("session cwd = %q, want the channel workdir", sess.CWD)
	}

	// /help works after pairing.
	help := chat
	help.Text = "/help"
	srv.handleChannelMessage(ctx, ad, help)
	ad.waitFor(t, "/resume", 5*time.Second)

	// A normal message runs the agent and replies with the answer.
	msg := chat
	msg.Text = "hi there"
	srv.handleChannelMessage(ctx, ad, msg)
	ad.waitFor(t, "mock: hi there", 30*time.Second)

	// /status reports the binding.
	status := chat
	status.Text = "/status"
	srv.handleChannelMessage(ctx, ad, status)
	ad.waitFor(t, "session:", 5*time.Second)

	// The transcript records the user turn.
	entries, err := srv.Store().AllEntries(sess.ID)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindMessage && e.Role == "user" && e.Message().Text == "hi there" {
			found = true
		}
	}
	if !found {
		t.Fatal("channel message was not persisted")
	}
}

func TestChannelPairingBlocksAfterFiveAttempts(t *testing.T) {
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Channel.Workdir = filepath.Join(home, "workdir")
	srv := startTestServer(t, cfg, config.Paths{Home: home})

	ad := &fakeAdapter{name: "fake"}
	srv.channels = []channel.Adapter{ad}
	pairing := channel.NewPairing("fake")
	srv.pairings["fake"] = pairing

	chat := channel.Inbound{Channel: "fake", ChatID: "chat-2", UserID: "u2", MessageID: "m1", Text: "/pair 999999"}
	for range 6 {
		srv.handleChannelMessage(context.Background(), ad, chat)
	}
	ad.waitFor(t, "too many attempts", 5*time.Second)
	if _, err := srv.Store().GetChannelState("fake", "chat-2"); err == nil {
		t.Fatal("chat was paired while rate limited")
	}
}

func TestRenderForPlainChannel(t *testing.T) {
	md := &fakeAdapter{name: "md", markdown: true}
	if body, mark := renderFor(md, "**bold** and `code`"); !mark || body != "**bold** and `code`" {
		t.Fatalf("markdown channel: body=%q markdown=%v", body, mark)
	}
	plain := &fakeAdapter{name: "plain", markdown: false}
	body, mark := renderFor(plain, "**bold** and `code`")
	if mark {
		t.Fatal("plain channel reported markdown")
	}
	if strings.Contains(body, "**") || strings.Contains(body, "`") {
		t.Fatalf("markdown source leaked to a plain channel: %q", body)
	}
	if !strings.Contains(body, "bold") || !strings.Contains(body, "code") {
		t.Fatalf("plain text lost content: %q", body)
	}
}
