package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexblock/pig/internal/agent"
	"github.com/alexblock/pig/internal/channel"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/provider"
	"github.com/alexblock/pig/internal/store"
)

// slotAdapter is a channel that cannot edit messages and reports a fixed number
// of passive replies, like QQ.
type slotAdapter struct {
	name  string
	slots int

	mu   sync.Mutex
	sent []string
}

func (f *slotAdapter) Name() string { return f.name }

func (f *slotAdapter) Caps() channel.Caps {
	return channel.Caps{Edit: false, MaxChars: 100}
}

func (f *slotAdapter) Run(ctx context.Context, in chan<- channel.Inbound) error {
	<-ctx.Done()
	return nil
}

func (f *slotAdapter) Send(ctx context.Context, o channel.Outbound) (channel.Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, o.Text)
	if f.slots > 0 {
		f.slots--
	}
	return channel.Handle{ID: "h1"}, nil
}

func (f *slotAdapter) Update(ctx context.Context, h channel.Handle, o channel.Outbound) error {
	return nil
}

func (f *slotAdapter) Close() error { return nil }

func (f *slotAdapter) RemainingSlots(chatID, replyTo string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slots
}

func (f *slotAdapter) waitFor(t *testing.T, substr string, timeout time.Duration) string {
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

func (f *slotAdapter) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func TestProgressStatusText(t *testing.T) {
	ad := &fakeAdapter{name: "telegram"}
	p := newChatProgress(ad, "c1", "m1", 1000000)
	p.observe(agent.Event{Type: agent.EvToolStart, ToolName: "bash"})
	p.observe(agent.Event{Type: agent.EvToolEnd, ToolName: "bash"})
	p.observe(agent.Event{Type: agent.EvToolEnd, ToolName: "read"})
	p.observe(agent.Event{Type: agent.EvTodo, Phases: []store.TodoPhase{
		{Name: "Build", Items: []store.TodoItem{{Text: "x", Status: "active"}}},
	}})
	p.observe(agent.Event{Type: agent.EvUsage, Usage: &provider.Usage{Input: 48300}})

	got := p.statusLocked(42 * time.Second)
	for _, want := range []string{"⏳ 42s", "Build", "read", "2 tools", "ctx 48.3k/1.0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q missing %q", got, want)
		}
	}
}

func TestProgressDurations(t *testing.T) {
	cases := map[time.Duration]string{
		3 * time.Second:               "3s",
		92 * time.Second:              "1m32s",
		(2*time.Hour + 5*time.Minute): "2h05m",
	}
	for d, want := range cases {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := shortTokens(900); got != "900" {
		t.Errorf("shortTokens(900) = %q", got)
	}
	if got := shortTokens(132000); got != "132k" {
		t.Errorf("shortTokens(132000) = %q", got)
	}
	if got := shortTokens(1000000); got != "1.0m" {
		t.Errorf("shortTokens(1000000) = %q", got)
	}
}

// A channel without editing must not spend a passive reply on an empty status,
// and must send at most one milestone afterwards.
func TestProgressQQWaitsForWork(t *testing.T) {
	ad := &slotAdapter{name: "qq", slots: 5}
	p := newChatProgress(ad, "u:abc", "m1", 1000000)
	p.startedAt = time.Now().Add(-2 * time.Second)

	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 0 {
		t.Fatalf("status sent before any work: %v", ad.messages())
	}

	p.observe(agent.Event{Type: agent.EvToolEnd, ToolName: "bash"})
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 1 {
		t.Fatalf("want 1 status, got %v", ad.messages())
	}
	if !strings.Contains(ad.messages()[0], "bash") {
		t.Fatalf("status lacks the tool: %q", ad.messages()[0])
	}

	// Cooldown: nothing more before the milestone.
	p.observe(agent.Event{Type: agent.EvToolEnd, ToolName: "read"})
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 1 {
		t.Fatalf("want no update while cooling down, got %v", ad.messages())
	}

	// Too soon for the next status: at most one a minute.
	p.mu.Lock()
	p.lastStatusAt = time.Now().Add(-10 * time.Second)
	p.lastUpdate = time.Time{}
	p.mu.Unlock()
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 1 {
		t.Fatalf("status repeated inside a minute: %v", ad.messages())
	}

	// A minute later the second and last status goes out.
	p.mu.Lock()
	p.lastStatusAt = time.Now().Add(-statusEvery - time.Second)
	p.lastUpdate = time.Time{}
	p.mu.Unlock()
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 2 {
		t.Fatalf("want a second status, got %v", ad.messages())
	}

	// maxStatuses caps it: no third bubble even after another minute.
	p.mu.Lock()
	p.lastStatusAt = time.Now().Add(-2 * statusEvery)
	p.lastUpdate = time.Time{}
	p.mu.Unlock()
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 2 {
		t.Fatalf("third status sent: %v", ad.messages())
	}
}

// Below the slot floor no milestone is sent, so the answer keeps its replies.
func TestProgressSkipsMilestoneWhenSlotsAreTight(t *testing.T) {
	ad := &slotAdapter{name: "qq", slots: 2}
	p := newChatProgress(ad, "u:abc", "m1", 1000000)
	p.mu.Lock()
	p.startedAt = time.Now().Add(-2 * time.Minute)
	p.sentFirst = true
	p.handle = channel.Handle{ID: "h1"}
	p.lastUpdate = time.Time{}
	p.mu.Unlock()

	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(ad.messages()); n != 0 {
		t.Fatalf("status sent with too few slots left: %v", ad.messages())
	}
}

func TestBudgetChunks(t *testing.T) {
	ad := &slotAdapter{name: "qq", slots: 2}
	chunks := []string{"aaa", "bbb", "ccc", "ddd"}
	got := budgetChunks(ad, "u:abc", "m1", "ses_1", chunks)
	if len(got) != 2 {
		t.Fatalf("want 2 chunks, got %v", got)
	}
	if !strings.Contains(got[1], "session://ses_1") || !strings.Contains(got[1], "reply limit") {
		t.Fatalf("truncation marker missing: %q", got[1])
	}

	// Adapters without a slot limit keep every chunk.
	free := &fakeAdapter{name: "telegram"}
	if got := budgetChunks(free, "c1", "m1", "ses_1", chunks); len(got) != 4 {
		t.Fatalf("unexpected trimming: %v", got)
	}

	// No slots left means no send at all.
	spent := &slotAdapter{name: "qq", slots: 0}
	if got := budgetChunks(spent, "u:abc", "m1", "ses_1", chunks); len(got) != 0 {
		t.Fatalf("want no chunks, got %v", got)
	}
}

// A long run shows a progress line with the tool it is running.
func TestChannelProgressLine(t *testing.T) {
	// The second turn is slow so the status ticker fires while the run is live.
	t.Setenv("PIG_MOCK_SCRIPT", `{"turns":[{"text":"","tool":{"name":"bash","args":{"command":"echo hi"}}},{"text":"done","delay_ms":8000}]}`)
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Channel.Workdir = filepath.Join(home, "workdir")
	srv := startTestServer(t, cfg, config.Paths{Home: home})

	ad := &slotAdapter{name: "fake", slots: 5}
	srv.channels = []channel.Adapter{ad}
	pairing := channel.NewPairing("fake")
	srv.pairings["fake"] = pairing

	in := make(chan channel.Inbound, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.consumeChannel(ctx, ad, in)

	chat := channel.Inbound{Channel: "fake", ChatID: "chat-1", UserID: "u1", MessageID: "m1"}
	pair := chat
	pair.Text = "/pair " + pairing.Code()
	in <- pair
	ad.waitFor(t, "paired", 5*time.Second)

	msg := chat
	msg.MessageID = "m2"
	msg.Text = "run it"
	in <- msg

	line := ad.waitFor(t, "⏳", 20*time.Second)
	for _, want := range []string{"bash", "1 tools"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status %q missing %q", line, want)
		}
	}
	ad.waitFor(t, "done", 20*time.Second)
}

// A /stop sent while a run is active must reach the daemon immediately and stop
// the run instead of waiting for it to finish.
func TestChannelStopDuringRun(t *testing.T) {
	t.Setenv("PIG_MOCK_SCRIPT", `{"turns":[{"text":"slow answer","delay_ms":5000}]}`)
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Channel.Workdir = filepath.Join(home, "workdir")
	srv := startTestServer(t, cfg, config.Paths{Home: home})

	ad := &slotAdapter{name: "fake", slots: 20}
	srv.channels = []channel.Adapter{ad}
	pairing := channel.NewPairing("fake")
	srv.pairings["fake"] = pairing

	in := make(chan channel.Inbound, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.consumeChannel(ctx, ad, in)

	chat := channel.Inbound{Channel: "fake", ChatID: "chat-1", UserID: "u1", MessageID: "m1"}
	pair := chat
	pair.Text = "/pair " + pairing.Code()
	in <- pair
	ad.waitFor(t, "paired", 5*time.Second)

	state, err := srv.Store().GetChannelState("fake", "chat-1")
	if err != nil {
		t.Fatal(err)
	}

	msg := chat
	msg.MessageID = "m2"
	msg.Text = "please wait"
	in <- msg

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := srv.ActiveRun(state.SessionID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never started; messages: %v", ad.messages())
		}
		time.Sleep(20 * time.Millisecond)
	}

	stop := chat
	stop.MessageID = "m3"
	stop.Text = "/stop"
	in <- stop
	ad.waitFor(t, "stopping", 5*time.Second)
	ad.waitFor(t, "stopped after", 15*time.Second)

	for _, m := range ad.messages() {
		if strings.Contains(m, "slow answer") {
			t.Fatalf("stopped run still answered: %v", ad.messages())
		}
	}
}
