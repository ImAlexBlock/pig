package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ImAlexBlock/pig/internal/channel"
	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/store"
)

// storeOptions returns session options owned by (channelName, chatID).
func storeOptions(t *testing.T, channelName, chatID string) store.SessionOptions {
	t.Helper()
	return store.SessionOptions{
		CWD: "/tmp", Model: "mock/mock",
		OriginChannel: channelName, OriginChat: chatID,
	}
}

// Group messages are refused outright: one paired group would hand the owner's
// shell and files to every member.
func TestChannelRejectsGroups(t *testing.T) {
	srv, ad, _ := newLangServer(t, "zh")
	group := channel.Inbound{
		Channel: "fake", ChatID: "g:12345", UserID: "u2", MessageID: "m1",
		Text: "hello", IsGroup: true,
	}
	srv.handleChannelMessage(context.Background(), ad, group, nil)
	got := ad.waitFor(t, "仅支持私聊", 5*time.Second)
	if !strings.Contains(got, "仅支持私聊") {
		t.Fatalf("group not refused: %q", got)
	}
	if _, err := srv.Store().GetChannelState("fake", "g:12345"); err == nil {
		t.Fatal("group created channel state")
	}
}

// /sessions lists only this chat's sessions, and /resume refuses a session that
// belongs to another chat.
func TestChannelSessionsScoped(t *testing.T) {
	srv, ad, chat := newLangServer(t, "en")
	ctx := context.Background()

	// A session owned by a different chat.
	other, err := srv.Store().CreateSession(storeOptions(t, "fake", "chat-2"))
	if err != nil {
		t.Fatal(err)
	}

	list := chat
	list.MessageID = "m2"
	list.Text = "/sessions"
	srv.handleChannelMessage(ctx, ad, list, nil)
	out := ad.waitFor(t, "resume", 5*time.Second)
	if strings.Contains(out, other.ID) {
		t.Fatalf("/sessions leaked another chat's session: %q", out)
	}

	resume := chat
	resume.MessageID = "m3"
	resume.Text = "/resume " + other.ID
	srv.handleChannelMessage(ctx, ad, resume, nil)
	refused := ad.waitFor(t, "session not found", 5*time.Second)
	if !strings.Contains(refused, other.ID) {
		t.Fatalf("unexpected reply: %q", refused)
	}
	state, err := srv.Store().GetChannelState("fake", "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID == other.ID {
		t.Fatal("chat rebound to a foreign session")
	}
}

// A session created from the CLI (no origin) stays reachable from a paired chat.
func TestChannelResumeUnownedSession(t *testing.T) {
	srv, ad, chat := newLangServer(t, "en")
	free, err := srv.Store().CreateSession(storeOptions(t, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	in := chat
	in.MessageID = "m2"
	in.Text = "/resume " + free.ID
	srv.handleChannelMessage(context.Background(), ad, in, nil)
	ad.waitFor(t, "resumed "+free.ID, 5*time.Second)
}

// A session pointed at a directory the daemon cannot enter is repointed rather
// than failing every bash call (host paths handed to a containerised daemon).
func TestStartRunRepointsMissingCWD(t *testing.T) {
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Channel.Workdir = filepath.Join(home, "workdir")
	srv := startTestServer(t, cfg, config.Paths{Home: home})

	sess, err := srv.Store().CreateSession(store.SessionOptions{CWD: "/no/such/host/path", Model: "mock/mock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := srv.StartRun(runSpec{SessionID: sess.ID, Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, _, ok := srv.ActiveRun(sess.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	fixed, err := srv.Store().GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.CWD != cfg.Channel.Workdir {
		t.Fatalf("cwd = %q, want workdir", fixed.CWD)
	}
}

// A vision model must not be told that it can only read a path: it believes the
// text over the image that travels with the request.
func TestAttachmentNoteForVisionModel(t *testing.T) {
	srv, _, _ := newLangServer(t, "en")
	sess, err := srv.Store().CreateSession(storeOptions(t, "fake", "chat-1"))
	if err != nil {
		t.Fatal(err)
	}
	img := []channel.Attachment{{Kind: "image", Name: "p.jpg", Mime: "image/jpeg", Data: []byte("\xff\xd8\xff")}}
	ids, note := srv.saveInboundAttachments(sess.ID, img, true)
	if len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	if !strings.Contains(note, "image attached below") || strings.Contains(note, "artifact://") {
		t.Fatalf("vision note = %q", note)
	}

	// A text-only model still gets the artifact reference and the read hint.
	_, note = srv.saveInboundAttachments(sess.ID, img, false)
	if !strings.Contains(note, "artifact://") || !strings.Contains(note, "Read an attachment") {
		t.Fatalf("text note = %q", note)
	}

	// Non-image files always keep the path, even for a vision model.
	_, note = srv.saveInboundAttachments(sess.ID, []channel.Attachment{{Kind: "file", Name: "a.zip", Mime: "application/zip", Data: []byte("PK")}}, true)
	if !strings.Contains(note, "artifact://") {
		t.Fatalf("file note = %q", note)
	}
}
