package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/store"
)

// newLangServer starts a daemon with one fake channel and a paired chat.
func newLangServer(t *testing.T, lang string) (*Server, *fakeAdapter, channel.Inbound) {
	t.Helper()
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Channel.Workdir = filepath.Join(home, "workdir")
	cfg.Channel.Lang = lang
	srv := startTestServer(t, cfg, config.Paths{Home: home})

	ad := &fakeAdapter{name: "fake"}
	srv.channels = []channel.Adapter{ad}
	pairing := channel.NewPairing("fake")
	srv.pairings["fake"] = pairing

	chat := channel.Inbound{Channel: "fake", ChatID: "chat-1", UserID: "u1", MessageID: "m1"}
	in := chat
	in.Text = "/pair " + pairing.Code()
	srv.handleChannelMessage(context.Background(), ad, in, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := srv.Store().GetChannelState("fake", "chat-1"); err == nil && st.Paired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pairing did not complete; messages: %v", ad.messages())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return srv, ad, chat
}

func TestChannelLangSwitch(t *testing.T) {
	srv, ad, chat := newLangServer(t, "")
	ctx := context.Background()

	// Default is English.
	def := chat
	def.MessageID = "m1b"
	def.Text = "/resume"
	srv.handleChannelMessage(ctx, ad, def, nil)
	ad.waitFor(t, "usage: /resume <session-id>", 5*time.Second)

	// Switch to Traditional Chinese: the ack and later replies follow.
	set := chat
	set.MessageID = "m2"
	set.Text = "/lang zh-Hant"
	srv.handleChannelMessage(ctx, ad, set, nil)
	ad.waitFor(t, "語言：繁體中文", 5*time.Second)

	help := chat
	help.MessageID = "m3"
	help.Text = "/help"
	srv.handleChannelMessage(ctx, ad, help, nil)
	got := ad.waitFor(t, "指令", 5*time.Second)
	if !strings.Contains(got, "/lang [en|zh|zh-Hant] — 檢視或切換回覆語言") {
		t.Fatalf("help lacks the lang line: %q", got)
	}

	// The choice is stored for the chat, so a config default cannot override it.
	state, err := srv.Store().GetChannelState("fake", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Lang != "zh-Hant" {
		t.Fatalf("lang = %q", state.Lang)
	}

	show := chat
	show.MessageID = "m4"
	show.Text = "/lang"
	srv.handleChannelMessage(ctx, ad, show, nil)
	ad.waitFor(t, "語言：繁體中文", 5*time.Second)

	// Simplified Chinese, then a bad value keeps the current language.
	zh := chat
	zh.MessageID = "m5"
	zh.Text = "/lang zh-CN"
	srv.handleChannelMessage(ctx, ad, zh, nil)
	ad.waitFor(t, "语言：中文（简体）", 5*time.Second)

	bad := chat
	bad.MessageID = "m6"
	bad.Text = "/lang tlh"
	srv.handleChannelMessage(ctx, ad, bad, nil)
	ad.waitFor(t, "不支持的语言：tlh", 5*time.Second)

	if state, err := srv.Store().GetChannelState("fake", "chat-1"); err != nil || state.Lang != "zh" {
		t.Fatalf("lang after bad input = %q, %v", state.Lang, err)
	}
}

// The config default applies to chats that never chose a language.
func TestChannelLangConfigDefault(t *testing.T) {
	srv, ad, chat := newLangServer(t, "zh")
	in := chat
	in.Text = "/status"
	srv.handleChannelMessage(context.Background(), ad, in, nil)
	got := ad.waitFor(t, "会话：", 5*time.Second)
	if !strings.Contains(got, "模型：") {
		t.Fatalf("status not translated: %q", got)
	}
}

// run.start carries attachment artifact ids into the user turn, which is what
// lets a vision model see an inbound image.
func TestRunStartRecordsAttachments(t *testing.T) {
	t.Setenv("PIG_MOCK_SCRIPT", `{"turns":[{"text":"ok"}]}`)
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	srv := startTestServer(t, cfg, config.Paths{Home: home})

	sess, err := srv.createSession(home)
	if err != nil {
		t.Fatal(err)
	}
	art, err := srv.Store().PutArtifact(sess.ID, "image/png", []byte("\x89PNG\r\n\x1a\nfake"))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := srv.StartRun(runSpec{
		SessionID:   sess.ID,
		Prompt:      "what is in the picture",
		Attachments: []string{art.ID},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, _, ok := srv.ActiveRun(sess.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}

	entries, err := srv.Store().AllEntries(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.Kind == store.KindMessage && e.Role == "user" {
			got = e.Message().Attachments
		}
	}
	if len(got) != 1 || got[0] != art.ID {
		t.Fatalf("attachments = %v, want [%s]", got, art.ID)
	}
}
